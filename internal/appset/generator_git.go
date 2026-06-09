package appset

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"go.yaml.in/yaml/v3"
)

func evaluateGitGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Git.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := gitGeneratorParamSets(ctx.RepoRoot, ctx.ManifestPath, generator.Git, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, supported, err
}

func gitGeneratorParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if len(git.Directories) == 0 && len(git.Files) == 0 {
		return nil, unsupportedGeneratorDiagnostic(manifestPath), false, nil
	}
	if len(git.Directories) > 0 {
		directorySets, err := gitDirectoryParamSets(repoRoot, manifestPath, git, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, true, err
		}
		return directorySets, nil, true, nil
	}

	fileSets, fileDiags, err := gitFileParamSets(repoRoot, manifestPath, git, useGoTemplate, goTemplateOptions)
	if err != nil {
		return nil, fileDiags, true, err
	}
	return fileSets, fileDiags, true, nil
}

func gitDirectoryParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, error) {
	includes, excludes := splitDirectoryPatterns(git.Directories)
	matches, err := matchDirectories(repoRoot, includes, excludes)
	if err != nil {
		return nil, err
	}

	out := make([]generatorParamSet, 0, len(matches))
	for _, match := range matches {
		params, err := pathParams(match, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, fmt.Errorf("%s render %s: %w", manifestPath, match, err)
		}
		out = append(out, generatorParamSet{
			Params:      params,
			SourcePath:  match,
			SourcePaths: []string{match},
			Generator:   "git-directories",
		})
	}
	return out, nil
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

func gitFileParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	includes, excludes, diags := splitGitFilePatterns(manifestPath, git.Files)
	matches, matchDiags, err := matchGitFiles(repoRoot, includes, excludes, manifestPath)
	diags = append(diags, matchDiags...)
	if err != nil {
		return nil, diags, err
	}

	out := make([]generatorParamSet, 0, len(matches))
	for _, match := range matches {
		paramObjects, decodeDiags, err := gitFileParams(repoRoot, match, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions, manifestPath)
		diags = append(diags, decodeDiags...)
		if err != nil {
			return nil, diags, err
		}
		if len(paramObjects) == 0 {
			continue
		}
		for _, params := range paramObjects {
			out = append(out, generatorParamSet{
				Params:      params,
				SourcePath:  match,
				SourcePaths: []string{match},
				Generator:   "git-files",
			})
		}
	}
	return out, diags, nil
}

func splitGitFilePatterns(manifestPath string, files []argoappv1.GitFileGeneratorItem) ([]string, []string, []diagnostic.Diagnostic) {
	var includes []string
	var excludes []string
	var diags []diagnostic.Diagnostic
	for _, file := range files {
		cleaned, err := cleanGitFilePattern(file.Path)
		if err != nil {
			diags = append(diags, appsetDiagnostic(manifestPath, err.Error()))
			continue
		}
		if file.Exclude {
			excludes = append(excludes, cleaned)
			continue
		}
		includes = append(includes, cleaned)
	}
	return includes, excludes, diags
}

func cleanGitFilePattern(pattern string) (string, error) {
	if filepath.IsAbs(pattern) {
		return "", fmt.Errorf("git files pattern %q must be relative", pattern)
	}
	cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(pattern)))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("git files pattern %q must name files", pattern)
	}
	if pathsafety.SlashRelEscapes(cleaned) {
		return "", fmt.Errorf("git files pattern %q escapes repository root", pattern)
	}
	return cleaned, nil
}

func matchGitFiles(repoRoot string, includes, excludes []string, manifestPath string) ([]string, []diagnostic.Diagnostic, error) {
	var candidates []string
	var diags []diagnostic.Diagnostic
	err := filepath.WalkDir(repoRoot, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if abs == repoRoot {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, abs)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("git files path %q is a symlink and was skipped", rel)))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		return nil, diags, err
	}
	sort.Strings(candidates)

	var matches []string
	for _, rel := range candidates {
		included, err := matchesAnyGlob(includes, rel)
		if err != nil {
			return nil, diags, fmt.Errorf("match git files include pattern: %w", err)
		}
		excluded, err := matchesAnyGlob(excludes, rel)
		if err != nil {
			return nil, diags, fmt.Errorf("match git files exclude pattern: %w", err)
		}
		if included && !excluded {
			matches = append(matches, rel)
		}
	}
	return matches, diags, nil
}

func matchesAnyGlob(patterns []string, rel string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := doublestar.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("pattern %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
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

func gitFileParams(repoRoot, rel, prefix string, values map[string]string, useGoTemplate bool, goTemplateOptions []string, manifestPath string) ([]map[string]any, []diagnostic.Diagnostic, error) {
	fileObjects, diag, err := decodeGitFileParams(filepath.Join(repoRoot, filepath.FromSlash(rel)), rel, manifestPath)
	if err != nil || diag != nil {
		if diag == nil {
			return nil, nil, err
		}
		return nil, []diagnostic.Diagnostic{*diag}, nil
	}

	paramObjects := make([]map[string]any, 0, len(fileObjects))
	for _, fileValues := range fileObjects {
		params := map[string]any{}
		if useGoTemplate {
			maps.Copy(params, fileValues)
			addGoTemplateFilePathParams(params, rel, prefix)
		} else {
			flattenParams("", fileValues, params)
			addFlatFilePathParams(params, rel, prefix)
		}

		if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
			return nil, nil, err
		}
		paramObjects = append(paramObjects, params)
	}
	return paramObjects, nil, nil
}

func decodeGitFileParams(absPath, rel, manifestPath string) ([]map[string]any, *diagnostic.Diagnostic, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, nil, err
	}
	if isEmptyGitFileParamsContent(data) {
		return []map[string]any{{}}, nil, nil
	}

	mapping := map[string]any{}
	if err := yaml.Unmarshal(data, &mapping); err == nil {
		if mapping == nil {
			mapping = map[string]any{}
		}
		return []map[string]any{mapping}, nil, nil
	}

	var objects []map[string]any
	if err := yaml.Unmarshal(data, &objects); err != nil {
		diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files match %q is not valid YAML: %v", rel, err))
		return nil, &diag, nil
	}
	for i := range objects {
		if objects[i] == nil {
			objects[i] = map[string]any{}
		}
	}
	return objects, nil, nil
}

func isEmptyGitFileParamsContent(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	return trimmed == "" || trimmed == "---" || trimmed == "..."
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

func addGoTemplateFilePathParams(params map[string]any, rel, prefix string) {
	dir := path.Dir(rel)
	segments := splitPathSegments(dir)
	filename := path.Base(rel)
	pathFields := map[string]any{
		"path":               dir,
		"basename":           path.Base(dir),
		"basenameNormalized": appsetutils.SanitizeName(path.Base(dir)),
		"filename":           filename,
		"filenameNormalized": appsetutils.SanitizeName(filename),
		"segments":           segments,
	}
	if prefix == "" {
		params["path"] = pathFields
		return
	}
	params[prefix] = map[string]any{"path": pathFields}
}

func addFlatFilePathParams(params map[string]any, rel, prefix string) {
	dir := path.Dir(rel)
	pathParamName := "path"
	if prefix != "" {
		pathParamName = prefix + "." + pathParamName
	}
	filename := path.Base(rel)
	params[pathParamName] = dir
	params[pathParamName+".basename"] = path.Base(dir)
	params[pathParamName+".basenameNormalized"] = appsetutils.SanitizeName(path.Base(dir))
	params[pathParamName+".filename"] = filename
	params[pathParamName+".filenameNormalized"] = appsetutils.SanitizeName(filename)
	for k, v := range splitPathSegments(dir) {
		if v != "" {
			params[pathParamName+"["+strconv.Itoa(k)+"]"] = v
		}
	}
}

func splitPathSegments(rel string) []string {
	if rel == "" {
		return []string{}
	}
	return strings.Split(rel, "/")
}

func flattenParams(prefix string, input map[string]any, out map[string]any) {
	for key, value := range input {
		flatKey := key
		if prefix != "" {
			flatKey = prefix + "." + key
		}
		flattenValue(flatKey, value, out)
	}
}

func flattenValue(key string, value any, out map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		flattenParams(key, typed, out)
	case []any:
		for i, item := range typed {
			flattenValue(key+"."+strconv.Itoa(i), item, out)
		}
	default:
		out[key] = fmt.Sprint(value)
	}
}

func cleanPathPattern(pattern string) string {
	return path.Clean(filepath.ToSlash(pattern))
}
