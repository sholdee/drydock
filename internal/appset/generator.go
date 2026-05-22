package appset

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type GeneratedApplication struct {
	Application argoappv1.Application
	SourcePath  string
	Generator   string
}

func GenerateFromYAML(repoRoot, manifestPath string, data []byte) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	var appset argoappv1.ApplicationSet
	if err := json.Unmarshal(normalized, &appset); err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	return Generate(repoRoot, manifestPath, appset)
}

func Generate(repoRoot, manifestPath string, appset argoappv1.ApplicationSet) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	git, diags, err := supportedGitDirectoriesGenerator(manifestPath, appset)
	if err != nil {
		return nil, diags, err
	}

	includes, excludes := splitDirectoryPatterns(git.Directories)
	matches, err := matchDirectories(repoRoot, includes, excludes)
	if err != nil {
		return nil, nil, err
	}

	out := make([]GeneratedApplication, 0, len(matches))
	for _, match := range matches {
		rendered, err := renderApplicationTemplate(appset, pathParams(match, git.PathParamPrefix))
		if err != nil {
			return nil, nil, fmt.Errorf("%s render %s: %w", manifestPath, match, err)
		}
		rendered.Namespace = appset.Namespace
		rendered.TypeMeta = metav1.TypeMeta{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
		}
		out = append(out, GeneratedApplication{
			Application: rendered,
			SourcePath:  match,
			Generator:   "git-directories",
		})
	}
	return out, nil, nil
}

func supportedGitDirectoriesGenerator(manifestPath string, appset argoappv1.ApplicationSet) (*argoappv1.GitGenerator, []diagnostic.Diagnostic, error) {
	if len(appset.Spec.Generators) != 1 {
		return nil, unsupportedGeneratorDiagnostic(manifestPath), fmt.Errorf("unsupported ApplicationSet generator in %s", manifestPath)
	}

	generator := appset.Spec.Generators[0]
	if generator.Git == nil {
		return nil, unsupportedGeneratorDiagnostic(manifestPath), fmt.Errorf("unsupported ApplicationSet generator in %s", manifestPath)
	}
	if len(generator.Git.Files) > 0 || len(generator.Git.Directories) == 0 {
		return nil, unsupportedGeneratorDiagnostic(manifestPath), fmt.Errorf("unsupported ApplicationSet git generator in %s", manifestPath)
	}
	return generator.Git, nil, nil
}

func unsupportedGeneratorDiagnostic(manifestPath string) []diagnostic.Diagnostic {
	return []diagnostic.Diagnostic{{
		Severity:   diagnostic.SeverityError,
		Category:   "appset",
		Message:    "only one git directories generator is supported in the MVP",
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.generators"},
	}}
}

func splitDirectoryPatterns(dirs []argoappv1.GitDirectoryGeneratorItem) ([]string, []string) {
	var includes []string
	var excludes []string
	for _, dir := range dirs {
		cleaned := cleanPathPattern(dir.Path)
		if dir.Exclude {
			excludes = append(excludes, cleaned)
			continue
		}
		includes = append(includes, cleaned)
	}
	return includes, excludes
}

func matchDirectories(repoRoot string, includes, excludes []string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(repoRoot, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") && abs != repoRoot {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)

		excluded, err := matchesAny(excludes, rel)
		if err != nil {
			return err
		}
		if excluded {
			return filepath.SkipDir
		}
		included, err := matchesAny(includes, rel)
		if err != nil {
			return err
		}
		if included {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func matchesAny(patterns []string, rel string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := path.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("match directory pattern %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func pathParams(rel, prefix string) map[string]any {
	segments := strings.Split(rel, "/")
	base := path.Base(rel)
	pathFields := map[string]any{
		"path":               rel,
		"basename":           base,
		"basenameNormalized": normalizeName(base),
		"segments":           segments,
	}
	if prefix == "" {
		return map[string]any{"path": pathFields}
	}
	return map[string]any{prefix: map[string]any{"path": pathFields}}
}

func normalizeName(input string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(input) {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func cleanPathPattern(pattern string) string {
	return path.Clean(filepath.ToSlash(pattern))
}

func cloneTemplateApp(app argoappv1.Application) (argoappv1.Application, error) {
	data, err := json.Marshal(app)
	if err != nil {
		return argoappv1.Application{}, err
	}
	var out argoappv1.Application
	if err := json.Unmarshal(data, &out); err != nil {
		return argoappv1.Application{}, err
	}
	return out, nil
}
