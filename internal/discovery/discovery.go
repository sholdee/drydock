package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/manifest"
)

type Options struct {
	AppManifestPaths []string
}

type ApplicationFile struct {
	Path        string
	Application argoappv1.Application
}

type SettingsCandidate struct {
	Path string
	Kind string
}

type Result struct {
	Applications       []ApplicationFile
	ApplicationSetPath []string
	SettingsCandidates []SettingsCandidate
}

func Scan(root string, opts Options) (Result, error) {
	var result Result
	roots := opts.AppManifestPaths
	if len(roots) == 0 {
		roots = []string{"."}
	}

	seen := map[string]struct{}{}
	for _, relRoot := range roots {
		start, err := scanStart(root, relRoot)
		if err != nil {
			return result, err
		}
		if err := scanPath(root, start, seen, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func scanPath(root, start string, seen map[string]struct{}, result *Result) error {
	info, err := os.Stat(start)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !isYAML(start) {
			return nil
		}
		return scanYAMLFileOnce(root, start, seen, result)
	}

	return filepath.WalkDir(start, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		return scanYAMLFileOnce(root, path, seen, result)
	})
}

func scanYAMLFileOnce(root, path string, seen map[string]struct{}, result *Result) error {
	rel, err := relativePath(root, path)
	if err != nil {
		return err
	}
	if _, ok := seen[rel]; ok {
		return nil
	}
	seen[rel] = struct{}{}
	return scanYAMLFile(path, rel, result)
}

func scanYAMLFile(path, rel string, result *Result) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	docs, err := manifest.DecodeDocuments(path, file)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		switch doc.Object.GetKind() {
		case "Application":
			var app argoappv1.Application
			if err := unstructuredToTyped(doc.Object.Object, &app); err != nil {
				return fmt.Errorf("%s: decode Application: %w", rel, err)
			}
			result.Applications = append(result.Applications, ApplicationFile{Path: rel, Application: app})
		case "ApplicationSet":
			result.ApplicationSetPath = append(result.ApplicationSetPath, rel)
		case "ConfigMap":
			if doc.Object.GetName() == "argocd-cm" {
				result.SettingsCandidates = append(result.SettingsCandidates, SettingsCandidate{Path: rel, Kind: "argocd-cm"})
			}
		case "Secret":
			if doc.Object.GetLabels()["argocd.argoproj.io/secret-type"] == "repository" {
				result.SettingsCandidates = append(result.SettingsCandidates, SettingsCandidate{Path: rel, Kind: "repository-secret"})
			}
		}
	}
	return nil
}

func scanStart(root, relRoot string) (string, error) {
	if filepath.IsAbs(relRoot) {
		return "", fmt.Errorf("app manifest path %q must be relative", relRoot)
	}
	joined := filepath.Join(root, relRoot)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("app manifest path %q escapes repository root", relRoot)
	}
	return joined, nil
}

func relativePath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(rel), nil
}

func shouldSkipDir(name string) bool {
	return name == ".git" || name == ".out" || strings.HasPrefix(name, ".cache")
}

func isYAML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func unstructuredToTyped(in map[string]any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
