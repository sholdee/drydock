package appset

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
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
		params, err := pathParams(match, git.PathParamPrefix, git.Values, appset.Spec.GoTemplate, appset.Spec.GoTemplateOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("%s render %s: %w", manifestPath, match, err)
		}
		rendered, err := renderApplicationTemplate(appset, params)
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
	var candidates []string
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
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, rel := range candidates {
		included, err := matchesAny(includes, rel)
		if err != nil {
			return nil, err
		}
		excluded, err := matchesAny(excludes, rel)
		if err != nil {
			return nil, err
		}
		if included && !excluded {
			matches = append(matches, rel)
		}
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

func pathParams(rel, prefix string, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	segments := strings.Split(rel, "/")
	base := path.Base(rel)

	params := map[string]any{}
	pathParamName := "path"
	if prefix != "" {
		pathParamName = prefix + "." + pathParamName
	}

	if useGoTemplate {
		pathFields := map[string]any{
			"path":               rel,
			"basename":           base,
			"basenameNormalized": appsetutils.SanitizeName(base),
			"segments":           segments,
		}
		if prefix == "" {
			params["path"] = pathFields
		} else {
			params[prefix] = map[string]any{"path": pathFields}
		}
	} else {
		params[pathParamName] = rel
		params[pathParamName+".basename"] = base
		params[pathParamName+".basenameNormalized"] = appsetutils.SanitizeName(base)
		for k, v := range segments {
			if v != "" {
				params[pathParamName+"["+strconv.Itoa(k)+"]"] = v
			}
		}
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func appendTemplatedValues(params map[string]any, values map[string]string, useGoTemplate bool, goTemplateOptions []string) error {
	if len(values) == 0 {
		return nil
	}

	renderer := &appsetutils.Render{}
	if useGoTemplate {
		renderedValues := map[string]any{}
		for key, value := range values {
			rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
			if err != nil {
				return fmt.Errorf("render value %q: %w", key, err)
			}
			renderedValues[key] = rendered
		}
		params["values"] = renderedValues
		return nil
	}

	for key, value := range values {
		rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
		if err != nil {
			return fmt.Errorf("render value %q: %w", key, err)
		}
		params["values."+key] = rendered
	}
	return nil
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
