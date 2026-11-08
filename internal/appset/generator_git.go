package appset

import (
	"fmt"
	"maps"
	"path"
	"sort"
	"strconv"
	"strings"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	sigsyaml "sigs.k8s.io/yaml"
)

// evaluateGitGenerator mirrors Argo CD v3.6.1
// applicationset/generators/git.go: it looks the AppProject up first
// (resolveSourceIntegrity), then runs the shared repo-source traversal of
// repo_path_utils.go over the repository the way the repo-server's
// GetGitDirectories and GetGitFiles list it.
func evaluateGitGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Git.Template)
	if err != nil {
		return nil, nil, true, err
	}
	if err := ctx.lookUpGitGeneratorProject(); err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := gitGeneratorParamSets(ctx, generator.Git)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, supported, err
}

// lookUpGitGeneratorProject mirrors GitGenerator.resolveSourceIntegrity
// (applicationset/generators/git.go:93-111 at v3.6.1). Before it lists
// anything, the Git generator gets the AppProject that the ApplicationSet's
// own spec.template.spec.project names, not the one of a generator
// template, from the controller namespace, and fails when it does not
// exist. It skips the lookup when the name holds "{{". An empty name never
// exists: the controller-runtime cache answers not found, so the
// ApplicationSet fails here. drydock cannot know the AppProjects of the
// cluster offline, so for any other name it records the name
// (GeneratedApplication.GitGeneratorProject), and discovery warns when the
// repository does not declare that AppProject.
func (ctx generatorContext) lookUpGitGeneratorProject() error {
	project := ctx.AppSet.Spec.Template.Spec.Project
	if strings.Contains(project, "{{") {
		return nil
	}
	if project == "" {
		return &generatorParamsError{err: fmt.Errorf("error getting project %s: %s", project, GitGeneratorProjectNotFoundMessage(project))}
	}
	if ctx.state != nil && ctx.state.gitProject == nil {
		ctx.state.gitProject = &project
	}
	return nil
}

// GitGeneratorProjectNotFoundMessage is the error that the Argo CD
// ApplicationSet controller's cached client returns for an AppProject that
// does not exist.
func GitGeneratorProjectNotFoundMessage(project string) string {
	return fmt.Sprintf("appprojects.argoproj.io %q not found", project)
}

func gitGeneratorParamSets(ctx generatorContext, git *argoappv1.GitGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if len(git.Directories) == 0 && len(git.Files) == 0 {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	tree := newGitGeneratorTree(ctx.RepoRoot, ctx.Options)
	useGoTemplate := ctx.AppSet.Spec.GoTemplate
	goTemplateOptions := ctx.AppSet.Spec.GoTemplateOptions
	// When both are set, directories win and files are ignored
	// (repo_path_utils.go generateRepoSourceParams).
	var (
		paramSets []generatorParamSet
		diags     []diagnostic.Diagnostic
		err       error
	)
	if len(git.Directories) > 0 {
		paramSets, diags, err = gitDirectoryParamSets(ctx, tree, git, useGoTemplate, goTemplateOptions)
	} else {
		paramSets, diags, err = gitFileParamSets(ctx, tree, git, useGoTemplate, goTemplateOptions)
	}
	if tree.scanned && tree.scanErr == nil {
		if empty := tree.emptySubmodules(); len(empty) > 0 {
			diags = append(diags, submoduleContentDiagnostic(ctx.ManifestPath, empty))
		}
	}
	if err != nil {
		return nil, diags, true, err
	}
	return paramSets, diags, true, nil
}

// submoduleContentDiagnostic warns about submodules that hold nothing in
// the tree the Git generator reads, while the Argo CD repo-server checks
// their content out (applicationsetcontroller.enable.git.submodule, on by
// default) and its Git generator sees it: a working tree whose submodule is
// not initialized, or a snapshot whose submodule drydock could not fetch.
func submoduleContentDiagnostic(manifestPath string, empty []string) diagnostic.Diagnostic {
	return appsetDiagnostic(manifestPath, fmt.Sprintf("submodule %s holds nothing in the tree that drydock reads; Argo CD checks out submodule content (applicationsetcontroller.enable.git.submodule) and its Git generator sees it; in a working tree, run git submodule update --init --recursive, or diff a Git ref with --ref", strings.Join(quoteAll(empty), ", ")))
}

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Quote(value))
	}
	return out
}

// gitDirectoryParamSets mirrors generateRepoSourceDirectoryParams: the
// repo-server lists every directory of the checkout, and filterPaths keeps
// the ones the patterns select, in walk order.
func gitDirectoryParamSets(ctx generatorContext, tree *gitRepositoryTree, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	allPaths, err := tree.directories(ctx.Options.IncludeHiddenDirectories)
	if err != nil {
		return nil, nil, fmt.Errorf("%s git generator: list directories: %w", ctx.ManifestPath, err)
	}
	patterns := make([]repoPathPattern, 0, len(git.Directories))
	for _, dir := range git.Directories {
		patterns = append(patterns, repoPathPattern{Path: dir.Path, Exclude: dir.Exclude})
	}
	var diags []diagnostic.Diagnostic
	if ctx.Options.IncludeHiddenDirectories {
		diags = append(diags, gitDirectoryBelowDotGitDiagnostics(ctx.ManifestPath, patterns)...)
	}
	requested := filterRepoPaths(patterns, allPaths)
	out := make([]generatorParamSet, 0, len(requested))
	for _, match := range requested {
		params, err := pathParams(match, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, diags, fmt.Errorf("%s render %s: %w", ctx.ManifestPath, match, err)
		}
		sourcePaths := []string{match}
		if match == ".git" {
			// drydock never reads the .git directory, so it is no input.
			sourcePaths = nil
		}
		out = append(out, generatorParamSet{
			Params:      params,
			SourcePath:  match,
			SourcePaths: sourcePaths,
			Generator:   "git-directories",
		})
	}
	return out, diags, nil
}

// gitDirectoryBelowDotGitDiagnostics warns about an include pattern that
// can match a directory below .git. With reposerver.include.hidden.directories
// on, the repo-server walk also lists the directories of the checkout's
// .git directory, such as .git/refs. drydock lists .git itself, but never
// what is in it: its own .git, if there is one, is not the repo-server's.
func gitDirectoryBelowDotGitDiagnostics(manifestPath string, patterns []repoPathPattern) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, pattern := range patterns {
		if pattern.Exclude {
			continue
		}
		first, rest, found := strings.Cut(pattern.Path, "/")
		if !found || rest == "" {
			continue
		}
		if ok, err := path.Match(first, ".git"); err != nil || !ok {
			continue
		}
		diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("git directories pattern %q can match directories below .git: with reposerver.include.hidden.directories on, Argo CD lists the directories of its checkout's .git directory too, which drydock does not read", pattern.Path)))
	}
	return diags
}

// repoPathPattern is an include or exclude path pattern of a Git or OCI
// generator (repo_path_utils.go pathPattern).
type repoPathPattern struct {
	Path    string
	Exclude bool
}

// filterRepoPaths is Argo CD's filterPaths (repo_path_utils.go:70-101):
// patterns match with path.Match exactly as written, without any
// normalization, a pattern that path.Match refuses is skipped (Argo CD logs
// it), an exclude match always wins, and the input order is kept.
func filterRepoPaths(patterns []repoPathPattern, allPaths []string) []string {
	var out []string
	for _, appPath := range allPaths {
		include := false
		exclude := false
		for _, pattern := range patterns {
			match, err := path.Match(pattern.Path, appPath)
			if err != nil || !match {
				continue
			}
			if pattern.Exclude {
				exclude = true
			} else {
				include = true
			}
		}
		if include && !exclude {
			out = append(out, appPath)
		}
	}
	return out
}

// gitFileParamSets mirrors generateRepoSourceFileParams: the files of every
// include pattern are unioned, the files of every exclude pattern removed,
// and the rest are parsed in sorted path order. A file that does not parse
// fails the whole ApplicationSet.
func gitFileParamSets(ctx generatorContext, tree *gitRepositoryTree, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	var includes, excludes []string
	for _, file := range git.Files {
		if file.Exclude {
			excludes = append(excludes, file.Path)
		} else {
			includes = append(includes, file.Path)
		}
	}
	contents := map[string][]byte{}
	inputs := map[string][]string{}
	var diags []diagnostic.Diagnostic
	for _, pattern := range includes {
		files, fileInputs, fileDiags, err := tree.getFilesWithInputs(ctx.ManifestPath, pattern, ctx.Options.NewGitFileGlobbing)
		diags = append(diags, fileDiags...)
		if err != nil {
			return nil, diags, err
		}
		maps.Copy(contents, files)
		maps.Copy(inputs, fileInputs)
	}
	for _, pattern := range excludes {
		files, fileDiags, err := tree.getFiles(ctx.ManifestPath, pattern, ctx.Options.NewGitFileGlobbing)
		diags = append(diags, fileDiags...)
		if err != nil {
			return nil, diags, err
		}
		for rel := range files {
			delete(contents, rel)
		}
	}
	rels := make([]string, 0, len(contents))
	for rel := range contents {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	out := make([]generatorParamSet, 0, len(rels))
	for _, rel := range rels {
		fileObjects, err := decodeFileParamsContent(contents[rel])
		if err != nil {
			return nil, diags, &generatorParamsError{err: fmt.Errorf("error generating params from git: unable to process file '%s' from repository '%s': %w", rel, git.RepoURL, err)}
		}
		paramObjects, err := fileParamObjects(fileObjects, rel, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, diags, err
		}
		for _, params := range paramObjects {
			out = append(out, generatorParamSet{
				Params:      params,
				SourcePath:  rel,
				SourcePaths: inputs[rel],
				Generator:   "git-files",
			})
		}
	}
	return out, diags, nil
}

// fileParamObjects shapes decoded param-file objects into generator params:
// file fields (flattened outside Go-template mode), the path/filename params
// for rel, and templated values. Argo CD shares this shaping between the Git
// and OCI file generators (applicationset/generators/repo_path_utils.go
// parseFileParams).
func fileParamObjects(fileObjects []map[string]any, rel, prefix string, values map[string]string, useGoTemplate bool, goTemplateOptions []string) ([]map[string]any, error) {
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
			return nil, err
		}
		paramObjects = append(paramObjects, params)
	}
	return paramObjects, nil
}

// decodeFileParamsContent decodes one generator param file as Argo CD's
// parseFileParams does (repo_path_utils.go:155-187): first as one object,
// which also succeeds for an empty file, and else as a list of objects. A
// file that is neither fails with the same "unable to parse file" error.
func decodeFileParamsContent(data []byte) ([]map[string]any, error) {
	if isEmptyGitFileParamsContent(data) {
		return []map[string]any{{}}, nil
	}

	mapping := map[string]any{}
	if err := sigsyaml.Unmarshal(data, &mapping); err == nil {
		if mapping == nil {
			mapping = map[string]any{}
		}
		return []map[string]any{mapping}, nil
	}

	var objects []map[string]any
	if err := sigsyaml.Unmarshal(data, &objects); err != nil {
		return nil, fmt.Errorf("unable to parse file: %w", err)
	}
	for i := range objects {
		if objects[i] == nil {
			objects[i] = map[string]any{}
		}
	}
	return objects, nil
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
