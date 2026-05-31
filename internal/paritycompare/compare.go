package paritycompare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/sholdee/drydock/internal/manifest"
	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Options struct {
	ArgoCDDir  string
	DrydockDir string
	OutDir     string
	IgnoreFile string
}

type Result struct {
	Applications int
	Resources    int
	Differences  int
	DiffFiles    []string
}

type ignoreConfig struct {
	JSONPointers []string `yaml:"jsonPointers"`
}

type appResources map[string]string

func Compare(options Options) (Result, error) {
	if strings.TrimSpace(options.ArgoCDDir) == "" {
		return Result{}, errors.New("argocd dir is required")
	}
	if strings.TrimSpace(options.DrydockDir) == "" {
		return Result{}, errors.New("drydock dir is required")
	}
	if strings.TrimSpace(options.OutDir) == "" {
		return Result{}, errors.New("out dir is required")
	}

	ignores, err := loadIgnorePointers(options.IgnoreFile)
	if err != nil {
		return Result{}, err
	}
	argocd, err := loadDir(options.ArgoCDDir, ignores)
	if err != nil {
		return Result{}, fmt.Errorf("load argocd manifests: %w", err)
	}
	drydock, err := loadDir(options.DrydockDir, ignores)
	if err != nil {
		return Result{}, fmt.Errorf("load drydock manifests: %w", err)
	}
	if err := os.RemoveAll(options.OutDir); err != nil {
		return Result{}, fmt.Errorf("clear output dir: %w", err)
	}
	if err := os.MkdirAll(options.OutDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output dir: %w", err)
	}

	var result Result
	apps := unionKeys(argocd, drydock)
	result.Applications = len(apps)
	for _, app := range apps {
		left := argocd[app]
		right := drydock[app]
		resources := unionKeys(left, right)
		result.Resources += len(resources)
		for _, resource := range resources {
			leftBody := left[resource]
			rightBody := right[resource]
			if err := writeCanonical(options.OutDir, "argocd-canonical", app, resource, leftBody); err != nil {
				return Result{}, err
			}
			if err := writeCanonical(options.OutDir, "drydock-canonical", app, resource, rightBody); err != nil {
				return Result{}, err
			}
			if leftBody == rightBody {
				continue
			}
			diffFile, err := writeDiff(options.OutDir, app, resource, leftBody, rightBody)
			if err != nil {
				return Result{}, err
			}
			result.Differences++
			result.DiffFiles = append(result.DiffFiles, diffFile)
		}
	}
	return result, nil
}

func loadIgnorePointers(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ignore file: %w", err)
	}
	var config ignoreConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse ignore file: %w", err)
	}
	for _, pointer := range config.JSONPointers {
		if err := validateJSONPointer(pointer); err != nil {
			return nil, err
		}
	}
	return append([]string(nil), config.JSONPointers...), nil
}

func loadDir(root string, ignores []string) (map[string]appResources, error) {
	out := make(map[string]appResources)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !isYAMLFile(path) {
			return nil
		}
		app, err := appNameFor(root, path)
		if err != nil {
			return err
		}
		resources, err := loadFile(path, ignores)
		if err != nil {
			return err
		}
		if out[app] == nil {
			out[app] = make(appResources)
		}
		maps.Copy(out[app], resources)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isYAMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func appNameFor(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve app name for %s: %w", path, err)
	}
	rel = filepath.ToSlash(rel)
	ext := filepath.Ext(rel)
	return strings.TrimSuffix(rel, ext), nil
}

func loadFile(path string, ignores []string) (appResources, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	docs, err := manifest.DecodeDocuments(path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out := make(appResources)
	for _, doc := range docs {
		obj := doc.Object.DeepCopy()
		for _, pointer := range ignores {
			if err := removeJSONPointer(obj.Object, pointer); err != nil {
				return nil, fmt.Errorf("%s %s: %w", path, manifest.IdentityOf(obj), err)
			}
		}
		key := manifest.IdentityOf(obj).String()
		body, err := canonicalBody(obj)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", path, key, err)
		}
		out[key] = body
	}
	return out, nil
}

func canonicalBody(obj *unstructured.Unstructured) (string, error) {
	data, err := json.MarshalIndent(obj.Object, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func removeJSONPointer(root map[string]any, pointer string) error {
	if err := validateJSONPointer(pointer); err != nil {
		return err
	}
	parts := strings.Split(pointer[1:], "/")
	current := root
	for i, raw := range parts {
		part, err := unescapeJSONPointer(raw)
		if err != nil {
			return err
		}
		if i == len(parts)-1 {
			delete(current, part)
			return nil
		}
		next, ok := current[part]
		if !ok {
			return nil
		}
		nextMap, ok := next.(map[string]any)
		if !ok {
			return nil
		}
		current = nextMap
	}
	return nil
}

func validateJSONPointer(pointer string) error {
	if pointer == "" {
		return errors.New("empty JSON pointer")
	}
	if pointer == "/" {
		return errors.New("root JSON pointer is not supported")
	}
	if !strings.HasPrefix(pointer, "/") {
		return fmt.Errorf("JSON pointer %q must start with /", pointer)
	}
	for part := range strings.SplitSeq(pointer[1:], "/") {
		if _, err := unescapeJSONPointer(part); err != nil {
			return err
		}
	}
	return nil
}

func unescapeJSONPointer(value string) (string, error) {
	for i := 0; i < len(value); i++ {
		if value[i] == '~' {
			if i == len(value)-1 || (value[i+1] != '0' && value[i+1] != '1') {
				return "", fmt.Errorf("invalid JSON pointer escape in %q", value)
			}
			i++
		}
	}
	value = strings.ReplaceAll(value, "~1", "/")
	value = strings.ReplaceAll(value, "~0", "~")
	return value, nil
}

func unionKeys[V any](left, right map[string]V) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		set[key] = struct{}{}
	}
	for key := range right {
		set[key] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeCanonical(root, side, app, resource, body string) error {
	if body == "" {
		return nil
	}
	path := filepath.Join(root, side, safeName(app), safeName(resource)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s dir: %w", side, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write canonical %s: %w", side, err)
	}
	return nil
}

func writeDiff(root, app, resource, leftBody, rightBody string) (string, error) {
	path := filepath.Join(root, "diffs", safeName(app), safeName(resource)+".diff")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create diff dir: %w", err)
	}
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(leftBody),
		B:        difflib.SplitLines(rightBody),
		FromFile: "argocd/" + app + "/" + resource,
		ToFile:   "drydock/" + app + "/" + resource,
		Context:  5,
	})
	if err != nil {
		return "", fmt.Errorf("create diff: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write diff: %w", err)
	}
	return path, nil
}

func safeName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unnamed"
	}
	return builder.String()
}
