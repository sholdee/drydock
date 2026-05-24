package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Options struct {
	AppManifestPaths []string
}

type ApplicationFile struct {
	Path        string
	Application argoappv1.Application
}

type ProjectFile struct {
	Path    string
	Project argoappv1.AppProject
}

type SettingsCandidate struct {
	Path string
	Kind string
}

type Result struct {
	Applications       []ApplicationFile
	ApplicationSetPath []string
	SettingsCandidates []SettingsCandidate
	Projects           []ProjectFile
}

func Scan(root string, opts Options) (Result, error) {
	var result Result
	roots := opts.AppManifestPaths
	explicit := len(roots) > 0
	if !explicit {
		roots = []string{"."}
	}

	seen := map[string]struct{}{}
	for _, relRoot := range roots {
		start, err := scanStart(root, relRoot)
		if err != nil {
			return result, err
		}
		if explicit {
			if err := rejectSymlinkComponents(root, start); err != nil {
				return result, err
			}
		}
		if err := scanPath(root, start, explicit, seen, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func scanPath(root, start string, explicit bool, seen map[string]struct{}, result *Result) error {
	info, err := os.Lstat(start)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if explicit {
			rel, relErr := relativePath(root, start)
			if relErr != nil {
				return relErr
			}
			return fmt.Errorf("app manifest path %q is a symlink", rel)
		}
		return nil
	}
	if !info.IsDir() {
		if !isYAML(start) {
			return nil
		}
		return scanYAMLFileOnce(root, start, explicit, seen, result)
	}

	return filepath.WalkDir(start, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
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
		return scanYAMLFileOnce(root, path, explicit, seen, result)
	})
}

func scanYAMLFileOnce(root, path string, explicit bool, seen map[string]struct{}, result *Result) error {
	rel, err := relativePath(root, path)
	if err != nil {
		return err
	}
	if _, ok := seen[rel]; ok {
		return nil
	}
	seen[rel] = struct{}{}
	if !explicit {
		candidate, err := looksLikeCandidate(path)
		if err != nil {
			return err
		}
		if !candidate {
			return nil
		}
	}
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
		if err := scanDocument(rel, doc.Object, result); err != nil {
			return err
		}
	}
	return nil
}

func scanDocument(rel string, obj *unstructured.Unstructured, result *Result) error {
	switch {
	case isArgoGVK(obj, "Application"):
		var app argoappv1.Application
		if err := unstructuredToTyped(obj.Object, &app); err != nil {
			return fmt.Errorf("%s: decode Application: %w", rel, err)
		}
		result.Applications = append(result.Applications, ApplicationFile{Path: rel, Application: app})
	case isArgoGVK(obj, "ApplicationSet"):
		result.ApplicationSetPath = append(result.ApplicationSetPath, rel)
	case isArgoGVK(obj, "AppProject"):
		var project argoappv1.AppProject
		if err := unstructuredToTyped(obj.Object, &project); err != nil {
			return fmt.Errorf("%s: decode AppProject: %w", rel, err)
		}
		result.Projects = append(result.Projects, ProjectFile{Path: rel, Project: project})
	case isCoreGVK(obj, "ConfigMap") && obj.GetName() == "argocd-cm":
		result.SettingsCandidates = append(result.SettingsCandidates, SettingsCandidate{Path: rel, Kind: "argocd-cm"})
	case isCoreGVK(obj, "Secret") && obj.GetLabels()["argocd.argoproj.io/secret-type"] == "repository":
		result.SettingsCandidates = append(result.SettingsCandidates, SettingsCandidate{Path: rel, Kind: "repository-secret"})
	case isArgoHelmValuesSettings(obj):
		result.SettingsCandidates = append(result.SettingsCandidates, SettingsCandidate{Path: rel, Kind: "argocd-values"})
	}
	return nil
}

func isArgoGVK(obj *unstructured.Unstructured, kind string) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "argoproj.io" && gvk.Version == "v1alpha1" && gvk.Kind == kind
}

func isCoreGVK(obj *unstructured.Unstructured, kind string) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "" && gvk.Version == "v1" && gvk.Kind == kind
}

func isArgoHelmValuesSettings(obj *unstructured.Unstructured) bool {
	configs, ok := obj.Object["configs"].(map[string]any)
	if !ok {
		return false
	}
	cm, ok := configs["cm"].(map[string]any)
	if !ok {
		return false
	}
	for key := range cm {
		if isKnownArgoCMSettingKey(key) {
			return true
		}
	}
	return false
}

func isKnownArgoCMSettingKey(key string) bool {
	switch key {
	case "kustomize.buildOptions",
		"application.resourceTrackingMethod",
		"application.instanceLabelKey",
		"resource.exclusions",
		"resource.inclusions",
		"resource.compareoptions",
		"resource.customizations":
		return true
	default:
		return strings.HasPrefix(key, "resource.customizations.")
	}
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

func rejectSymlinkComponents(root, target string) error {
	rel, err := relativePath(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("app manifest path %q includes symlink component %q", rel, component)
		}
	}
	return nil
}

func looksLikeCandidate(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(data)
	return strings.Contains(text, "argoproj.io/v1alpha1") ||
		strings.Contains(text, "argocd-cm") ||
		strings.Contains(text, "argocd.argoproj.io/secret-type") ||
		(strings.Contains(text, "configs:") && strings.Contains(text, "cm:")), nil
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
