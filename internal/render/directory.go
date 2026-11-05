package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/manifestyaml"
	"github.com/sholdee/drydock/internal/pathsafety"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type DirectoryRenderer struct{}

const (
	argocdSkipFileRenderingMarker = "+argocd:skip-file-rendering"
	drydockCacheMetadataDirName   = ".drydock-cache"

	// DirectoryExtensionFilterDisabledCode identifies the warning for a
	// directory source that sets disableExtensionFilter with neither include
	// nor exclude. Argo CD's repo-server logs the same condition as a warning
	// and renders every file (getPotentiallyValidManifests,
	// reposerver/repository/repository.go at v3.6.1).
	DirectoryExtensionFilterDisabledCode = "directory.extension-filter-disabled"
)

// directoryManifestFile is Argo CD's built-in directory extension filter
// (reposerver/repository/repository.go manifestFile at v3.6.1), matched
// case-sensitively against the file name: deploy.YAML is not a manifest.
var directoryManifestFile = regexp.MustCompile(`^.*\.(yaml|yml|json|jsonnet)$`)

var utf8ByteOrderMark = []byte("\xef\xbb\xbf")

//nolint:gocyclo // Directory rendering keeps walk, filtering, decode, and path provenance in one pass.
func (DirectoryRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	var out []Manifest
	diags := directoryRenderDiagnostics(opts)

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDirectoryCandidate(root, path, entry, opts) {
				return filepath.SkipDir
			}
			return nil
		}
		// disableExtensionFilter skips the built-in extension filter, leaving
		// include and exclude as the only filters (Argo CD v3.6.1
		// getPotentiallyValidManifestFile).
		if !opts.DirectoryDisableExtensionFilter && !isManifestFile(path) {
			return nil
		}
		if !directoryManifestIncluded(root, path, opts) {
			return nil
		}

		manifestPath, pathErr := relativeManifestPath(source.RepoRoot, path)
		if pathErr != nil {
			return pathErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(argocdSkipFileRenderingMarker)) {
			return nil
		}
		if isJsonnetFile(path) {
			rendered, err := renderJsonnetFile(root, source.RepoRoot, path, manifestPath, opts)
			if err != nil {
				return err
			}
			out = append(out, rendered...)
			return nil
		}
		// The repo-server opens every directory file through
		// utfutil.OpenFile(..., utfutil.UTF8), which drops a leading UTF-8 byte
		// order mark before the JSON or YAML decoder sees the content.
		data = bytes.TrimPrefix(data, utf8ByteOrderMark)
		jsonFile := isJSONSuffixFile(path)
		if jsonFile {
			if err := validateDirectoryJSONFile(manifestPath, data); err != nil {
				return err
			}
		}
		roots, err := manifest.DecodeDocumentRoots(manifestPath, bytes.NewReader(data))
		if err != nil {
			if jsonFile || directoryDecodeFailureIsFatal(data) {
				return err
			}
			return nil
		}
		hasCandidate, err := classifyDirectoryRoots(roots)
		if err != nil {
			return err
		}
		if !hasCandidate {
			return nil
		}
		docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(data))
		if err != nil {
			return err
		}

		for _, doc := range docs {
			if doc.RootObject != nil && doc.RootObject != doc.Object {
				if _, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.RootObject}); err != nil {
					return err
				}
			}
			rendered := NewDocumentManifest(doc)
			include, err := classifyDirectoryDocument(rendered)
			if err != nil {
				return err
			}
			if !include {
				continue
			}
			out = append(out, rendered)
		}
		return nil
	})
	return out, diags, err
}

func directoryRenderDiagnostics(opts RenderOptions) []diagnostic.Diagnostic {
	if !opts.DirectoryDisableExtensionFilter || opts.DirectoryInclude != "" || opts.DirectoryExclude != "" {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Code:     DirectoryExtensionFilterDisabledCode,
		Severity: diagnostic.SeverityWarning,
		Category: "directory",
		Message:  "directory.disableExtensionFilter is enabled without include or exclude; every file in the directory is read and considered as a manifest",
	}}
}

func classifyDirectoryRoots(docs []manifest.Document) (bool, error) {
	hasCandidate := false
	for _, doc := range docs {
		include, err := classifyDirectoryDocument(Manifest{Path: doc.Path, Object: doc.Object})
		if err != nil {
			return false, err
		}
		if include {
			hasCandidate = true
		}
	}
	return hasCandidate, nil
}

func classifyDirectoryDocument(manifest Manifest) (bool, error) {
	if manifest.Object == nil {
		return false, nil
	}
	apiVersion := manifest.Object.GetAPIVersion()
	kind := manifest.Object.GetKind()
	if apiVersion == "" && kind == "" {
		return false, nil
	}
	if apiVersion == "" {
		return false, fmt.Errorf("%s document is missing apiVersion", manifest.Path)
	}
	if kind == "" {
		return false, fmt.Errorf("%s document is missing kind", manifest.Path)
	}
	return true, nil
}

// directoryDecodeFailureIsFatal mirrors how getObjsFromYAMLOrJSON
// (reposerver/repository/repository.go at v3.6.1) treats a file outside the
// ".json" rule whose YAML/JSON stream fails to decode. The source fails when
// the stream yielded an object before the failure, or when the raw content
// holds all of "apiVersion:", "kind:" and "metadata:"; any other file is
// taken to be unrelated YAML and skipped.
func directoryDecodeFailureIsFatal(data []byte) bool {
	if directoryStreamYieldedObject(data) {
		return true
	}
	return bytes.Contains(data, []byte("apiVersion:")) &&
		bytes.Contains(data, []byte("kind:")) &&
		bytes.Contains(data, []byte("metadata:"))
}

// directoryStreamYieldedObject reports whether the repo-server's
// splitYAMLOrJSON returns at least one object before it stops. It decodes
// each document into an Unstructured, so it stops at the first decode error
// and at the first non-empty document that is not an object with a string
// kind; null and empty documents are skipped.
func directoryStreamYieldedObject(data []byte) bool {
	decoder := manifestyaml.NewDecoder(bytes.NewReader(data))
	for {
		value, err := decoder.Decode()
		if err != nil {
			return false
		}
		if value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if kind, _ := object["kind"].(string); kind == "" {
			return false
		}
		return true
	}
}

func directoryManifestIncluded(root, filePath string, opts RenderOptions) bool {
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if opts.DirectoryExclude != "" && argoglob.Match(opts.DirectoryExclude, rel) {
		return false
	}
	if opts.DirectoryInclude != "" && !argoglob.Match(opts.DirectoryInclude, rel) {
		return false
	}
	return true
}

func shouldSkipDirectoryCandidate(root, path string, entry os.DirEntry, opts RenderOptions) bool {
	if path == root {
		return false
	}
	// Repository metadata is never a manifest; sourceRoot already refuses a
	// source path inside .git.
	if entry.Name() == drydockCacheMetadataDirName || strings.EqualFold(entry.Name(), ".git") {
		return true
	}
	return !opts.DirectoryRecurse
}

func isManifestFile(path string) bool {
	return directoryManifestFile.MatchString(filepath.Base(path))
}

// isJSONSuffixFile reports whether the repo-server's case-sensitive ".json"
// suffix check selects the single-object JSON rule for a file, whether or not
// the extension filter is disabled.
func isJSONSuffixFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".json")
}

// validateDirectoryJSONFile mirrors the repo-server's rule for a file whose
// name ends in ".json" (getObjsFromYAMLOrJSON at v3.6.1): the content, after
// any UTF-8 byte order mark, must decode as one JSON object with a kind,
// falling back to an OCI image manifest, and nothing but whitespace may
// follow it. An empty file, YAML, a JSON array or scalar, and a syntax error
// all fail the source. A file accepted only as an OCI image manifest is left
// to the stream decoder, which skips an object without apiVersion and kind.
func validateDirectoryJSONFile(manifestPath string, data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object unstructured.Unstructured
	if err := decoder.Decode(&object); err != nil {
		var ociManifest imagev1.Manifest
		if json.NewDecoder(bytes.NewReader(data)).Decode(&ociManifest) != nil {
			return fmt.Errorf("%s: decode JSON object failed: %s", manifestPath, describeDirectoryJSONDecodeError(err))
		}
	}
	if decoder.More() {
		return fmt.Errorf("%s: found multiple objects; only single objects are allowed in JSON files", manifestPath)
	}
	return nil
}

// describeDirectoryJSONDecodeError names why a ".json" file did not decode
// without echoing its content: Unstructured's missing-kind error quotes the
// whole document.
func describeDirectoryJSONDecodeError(err error) string {
	if errors.Is(err, io.EOF) {
		return "file is empty"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected end of JSON input"
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("invalid JSON at offset %d", syntaxErr.Offset)
	}
	if runtime.IsMissingKind(err) {
		return "JSON object is missing kind"
	}
	return "JSON value is not an object"
}

// isJsonnetFile mirrors the repo-server's case-sensitive ".jsonnet" suffix
// check, which applies whether or not the extension filter is disabled.
func isJsonnetFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".jsonnet")
}

func sourceRoot(source ResolvedSource) (string, error) {
	sourcePath, err := cleanSourcePath(source.Path)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return "", err
	}
	return filepath.Join(source.RepoRoot, sourcePath), nil
}

func cleanSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes repository root", path)
	}
	if pathsafety.RelEntersGit(clean) {
		return "", gitPathRefError("source path", path)
	}
	return clean, nil
}

func rejectSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for component := range strings.SplitSeq(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func relativeManifestPath(repoRoot, path string) (string, error) {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("manifest path %q escapes repository root %q", path, repoRoot)
	}
	return rel, nil
}
