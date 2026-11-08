package appset

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// gitFilesAppSet is an ApplicationSet with one Git files generator. Each
// generated Application is named after the directory and file of its param
// file, so the names show which files matched.
func gitFilesAppSet(patterns ...string) []byte {
	var files strings.Builder
	for _, pattern := range patterns {
		exclude := strings.HasPrefix(pattern, "!")
		files.WriteString("          - path: '" + strings.TrimPrefix(pattern, "!") + "'\n")
		if exclude {
			files.WriteString("            exclude: true\n")
		}
	}
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-files
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://example.invalid/repo.git
        files:
` + files.String() + `  template:
    metadata:
      name: '{{ .path.path | replace "/" "-" | replace "." "root" | lower }}-{{ .path.filenameNormalized | replace "." "-" }}'
    spec:
      project: default
`)
}

// gitDirectoriesAppSet is an ApplicationSet with one Git directories
// generator whose Applications are named after the directory path.
func gitDirectoriesAppSet(patterns ...string) []byte {
	var dirs strings.Builder
	for _, pattern := range patterns {
		exclude := strings.HasPrefix(pattern, "!")
		dirs.WriteString("          - path: '" + strings.TrimPrefix(pattern, "!") + "'\n")
		if exclude {
			dirs.WriteString("            exclude: true\n")
		}
	}
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-directories
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://example.invalid/repo.git
        directories:
` + dirs.String() + `  template:
    metadata:
      name: 'dir-{{ .path.path | replace "/" "-" | replace "." "dot" }}'
    spec:
      project: default
`)
}

func writeGitParityTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range files {
		writeAppsetTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), "x: y\n")
	}
	return root
}

// TestGenerateGitFilesMatchesLikeGitLsFiles pins the default Git files
// generator of Argo CD v3.6.1, which lists files with `git ls-files --
// <pattern>` (util/git/client.go:763-770): "*" also matches "/", and a
// pattern that names a directory matches every file below it. The cases are
// the ones the gap audit found.
func TestGenerateGitFilesMatchesLikeGitLsFiles(t *testing.T) {
	root := writeGitParityTree(t,
		"apps/a/config.yaml",
		"apps/a/b/config.yaml",
		"apps/c/config.yaml",
		"apps/top.yaml",
		"apps/sub/nested.yaml",
		"other/config.yaml",
	)
	tests := []struct {
		pattern string
		want    []string
	}{
		{"apps/*/config.yaml", []string{"apps-a-b-config-yaml", "apps-a-config-yaml", "apps-c-config-yaml"}},
		{"apps/*.yaml", []string{"apps-a-b-config-yaml", "apps-a-config-yaml", "apps-c-config-yaml", "apps-sub-nested-yaml", "apps-top-yaml"}},
		{"apps", []string{"apps-a-b-config-yaml", "apps-a-config-yaml", "apps-c-config-yaml", "apps-sub-nested-yaml", "apps-top-yaml"}},
		{"./apps/sub", []string{"apps-sub-nested-yaml"}},
		{"apps/sub/", []string{"apps-sub-nested-yaml"}},
		{":(glob)apps/*/config.yaml", []string{"apps-a-config-yaml", "apps-c-config-yaml"}},
		{"", []string{"apps-a-b-config-yaml", "apps-a-config-yaml", "apps-c-config-yaml", "apps-sub-nested-yaml", "apps-top-yaml", "other-config-yaml"}},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet(tt.pattern))
			if err != nil || len(diags) != 0 {
				t.Fatalf("GenerateFromYAML() error = %v, diagnostics = %#v", err, diags)
			}
			got := generatedNames(apps)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("generated names = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGenerateGitFilesPathspecThatGitRefusesFailsWholeAppSet pins a pattern
// that `git ls-files` refuses: git exits with "fatal:", GetGitFiles fails,
// and so does the whole ApplicationSet. drydock used to skip such a
// pattern with a warning.
func TestGenerateGitFilesPathspecThatGitRefusesFailsWholeAppSet(t *testing.T) {
	root := writeGitParityTree(t, "apps/a/config.yaml")
	for _, pattern := range []string{"../apps/a/config.yaml", "/apps/a/config.yaml", ":(bogus)apps"} {
		t.Run(pattern, func(t *testing.T) {
			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet("apps/a/config.yaml", pattern))
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if len(apps) != 0 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.generator-failed" {
				t.Fatalf("apps = %q, diagnostics = %#v, want one appset.generator-failed warning", generatedNames(apps), diags)
			}
		})
	}
}

// hostSymlinkHops is the symlink limit of the kernel that runs the test:
// FilepathGlob follows symlinks until the kernel refuses.
func hostSymlinkHops() int {
	if runtime.GOOS == "darwin" {
		return 32
	}
	return 40
}

// TestGenerateGitFilesNewGlobbingMatchesFilepathGlob pins
// applicationsetcontroller.enable.new.git.file.globbing: LsFiles then
// matches with doublestar.FilepathGlob of the pattern joined to the
// checkout root (util/git/client.go:726-761). drydock globs over its own
// view of the tree; this compares it with that exact upstream call on the
// same directory, which holds a .git directory, a symlink to a directory
// with a symlink in it, and a symlink loop. When the upstream glob matches
// something in .git, drydock fails the ApplicationSet instead of reading
// it.
//
//nolint:gocyclo // One table drives both sides.
func TestGenerateGitFilesNewGlobbingMatchesFilepathGlob(t *testing.T) {
	root := writeGitParityTree(t,
		"apps/a/config.yaml",
		"apps/a/b/config.yaml",
		"apps/c/config.yaml",
		"apps/top.yaml",
		".hidden/x.yaml",
		"root.yaml",
		"real/one.yaml",
		"d2/x.yaml",
		"loop/y.yaml",
		".git/config",
		".git/HEAD",
		".git/info/exclude",
	)
	// The upstream glob counts the symlinks of the root path too, such as
	// /var on macOS.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// drydock knows the names of a checkout's .git directory
	// (checkoutGitPaths), not branch names below refs.
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"linkdir":       "real",
		"linkfile.yaml": "root.yaml",
		"l1":            "d1",
		"d1/l2":         "../d2",
		"loop/self":     ".",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	saved := maxRepoSymlinkHops
	maxRepoSymlinkHops = hostSymlinkHops()
	t.Cleanup(func() { maxRepoSymlinkHops = saved })
	tree := newGitRepositoryTree(root, nil)
	for _, pattern := range []string{
		"**", "*", ".", "**/*.yaml", "apps/**", "apps/*", "apps/**/config.yaml", "*/*",
		"linkdir/*", "linkdir", "**/one.yaml", "apps/*.yaml", "apps", "*.yaml", ".*/*",
		"**/", "apps/", "{apps,real}/*", "/apps/*.yaml", "apps/*/config.yaml", "[a]pps/top.yaml",
		"l1/*/*", "l1/l2/x.yaml", "**/x.yaml", "loop/*", "loop/self/*", "loop/self/self/y.yaml",
		"loop/**", "**/y.yaml", ".*", "**/config", "**/exclude", "**/heads", ".git", ".git/*",
	} {
		t.Run(pattern, func(t *testing.T) {
			upstream, err := doublestar.FilepathGlob(filepath.Join(root, pattern))
			if err != nil {
				t.Fatalf("FilepathGlob() error = %v", err)
			}
			var want []string
			inGit := false
			for _, match := range upstream {
				rel, err := filepath.Rel(root, match)
				if err != nil {
					t.Fatal(err)
				}
				rel = filepath.ToSlash(rel)
				inGit = inGit || rel == ".git" || strings.HasPrefix(rel, ".git/")
				want = append(want, rel)
			}
			listed, diags, err := tree.globFiles("app-set.yaml", pattern)
			if inGit {
				// Argo CD fails on a directory of .git, which drydock
				// reports as generator-failed, and reads a file there,
				// which drydock does not, so the ApplicationSet is
				// unsupported.
				_, failed := errors.AsType[*generatorParamsError](err)
				_, unsupported := errors.AsType[*generatorUnsupportedError](err)
				if !failed && !unsupported {
					t.Fatalf("FilepathGlob = %q matches .git; globFiles() error = %v, want an ApplicationSet failure", want, err)
				}
				return
			}
			if err != nil || len(diags) != 0 {
				t.Fatalf("globFiles() error = %v, diagnostics = %#v", err, diags)
			}
			var got []string
			for _, file := range listed {
				got = append(got, file.rel)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("globFiles() = %q, FilepathGlob = %q", got, want)
			}
		})
	}
}

// TestGenerateGitFilesNewGlobbingStopsAtSlashAndFailsOnDirectories pins two
// results of the new globbing: "*" does not match "/" here, and a matched
// directory fails the generator, because the repo-server reads every match
// with os.ReadFile.
func TestGenerateGitFilesNewGlobbingStopsAtSlashAndFailsOnDirectories(t *testing.T) {
	files := writeGitParityTree(t, "apps/a/config.yaml", "apps/a/b/config.yaml", "apps/c/config.yaml")
	apps, diags, err := GenerateWithOptionsFromYAML(t, files, gitFilesAppSet("apps/*/config.yaml"), Options{NewGitFileGlobbing: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("GenerateWithOptions() error = %v, diagnostics = %#v", err, diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"apps-a-config-yaml", "apps-c-config-yaml"}) {
		t.Fatalf("generated names = %q, want apps/a and apps/c only", got)
	}
	apps, diags, err = GenerateWithOptionsFromYAML(t, files, gitFilesAppSet("apps/*"), Options{NewGitFileGlobbing: true})
	if err != nil || len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "is a directory") {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want an is a directory generator failure", generatedNames(apps), diags, err)
	}
}

func GenerateWithOptionsFromYAML(t *testing.T, root string, data []byte, options Options) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	t.Helper()
	return GenerateFromYAMLWithOptions(root, "app-set.yaml", data, options)
}

// TestGenerateGitDirectoriesMatchPatternsAsWritten pins filterPaths
// (applicationset/generators/repo_path_utils.go:70-101 at v3.6.1): it
// matches each pattern with path.Match as written, so "./apps/*" and
// "apps/*/" match nothing, and it logs and skips a pattern that path.Match
// refuses. drydock used to clean the patterns and to fail the whole run on
// a malformed one.
func TestGenerateGitDirectoriesMatchPatternsAsWritten(t *testing.T) {
	root := writeGitParityTree(t, "apps/alpha/x.yaml", "apps/beta/x.yaml")
	tests := []struct {
		patterns []string
		want     []string
	}{
		{[]string{"./apps/*"}, nil},
		{[]string{"apps/*/"}, nil},
		{[]string{"apps//alpha"}, nil},
		{[]string{"apps/[", "apps/*"}, []string{"dir-apps-alpha", "dir-apps-beta"}},
		{[]string{"apps/*", "!apps/[", "!apps/beta"}, []string{"dir-apps-alpha"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.patterns, ","), func(t *testing.T) {
			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitDirectoriesAppSet(tt.patterns...))
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if tt.want == nil && len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if got := generatedNames(apps); !slices.Equal(got, tt.want) && (len(got) != 0 || tt.want != nil) {
				t.Fatalf("generated names = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGitRepositoryDirectoriesMatchCheckoutWalk pins the directory list of
// the Git directory generator to the repo-server's walkDirectoryTree over
// a checkout (reposerver/repository/utils.go:90-121 at v3.6.1): walk order,
// no symlinked directory, no hidden directory unless
// reposerver.include.hidden.directories, and, because a checkout holds no
// empty directory, no directory without a file below it. With hidden
// directories, the walk also lists the checkout's .git directory.
func TestGitRepositoryDirectoriesMatchCheckoutWalk(t *testing.T) {
	root := writeGitParityTree(t,
		"apps/alpha/kustomization.yaml",
		"apps/.hidden/kustomization.yaml",
		"apps/.hidden/nested/kustomization.yaml",
		"apps/only-hidden-file/.keep",
		"a/b/c.yaml",
		"a-c/d.yaml",
		".top/x.yaml",
		".git/config",
		"nested/.git/HEAD",
		"nested/sub/x.yaml",
	)
	for _, empty := range []string{"apps/empty", "apps/empty-parent/empty-child"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(empty)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("alpha", filepath.Join(root, "apps", "linked")); err != nil {
		t.Fatal(err)
	}

	tree := newGitRepositoryTree(root, nil)
	got, err := tree.directories(false)
	if err != nil {
		t.Fatal(err)
	}
	// nested holds its own .git, so it is a gitlink. Without .gitmodules,
	// Argo CD fetches no submodule content, and the checkout holds an empty
	// directory there.
	want := []string{"a", "a/b", "a-c", "apps", "apps/alpha", "apps/only-hidden-file", "nested"}
	if !slices.Equal(got, want) {
		t.Fatalf("directories(false) = %q, want %q", got, want)
	}
	got, err = tree.directories(true)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{".git", ".top", "a", "a/b", "a-c", "apps", "apps/.hidden", "apps/.hidden/nested", "apps/alpha", "apps/only-hidden-file", "nested"}
	if !slices.Equal(got, want) {
		t.Fatalf("directories(true) = %q, want %q", got, want)
	}
}

// TestGenerateGitDirectoriesIncludeHiddenDirectories pins
// reposerver.include.hidden.directories for the Git and OCI directory
// generators, and the warning for a pattern that can reach below .git.
func TestGenerateGitDirectoriesIncludeHiddenDirectories(t *testing.T) {
	root := writeGitParityTree(t, "apps/alpha/x.yaml", "apps/.hidden/x.yaml")
	apps, _, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dir-apps-alpha"}) {
		t.Fatalf("generated names = %q, want apps/alpha only", got)
	}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*", "*/refs"), Options{IncludeHiddenDirectories: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dir-apps-dothidden", "dir-apps-alpha"}) {
		t.Fatalf("generated names = %q, want apps/.hidden and apps/alpha", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, `"*/refs" can match directories below .git`) {
		t.Fatalf("diagnostics = %#v, want one below-.git warning", diags)
	}

	artifact := writeGitParityTree(t, "apps/alpha/x.yaml", "apps/.hidden/x.yaml")
	got, _, err := ociParamSetsJSON(t, artifact, `{"directories":[{"path":"apps/*"}]}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, ".hidden") {
		t.Fatalf("oci directories without hidden directories = %s", got)
	}
	hidden, err := walkDirectoryTree(artifact, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hidden, []string{"apps", "apps/.hidden", "apps/alpha"}) {
		t.Fatalf("walkDirectoryTree(true) = %q", hidden)
	}
}

// skipPaths is a WorktreeFilter that skips the listed paths and everything
// below them.
type skipPaths []string

func (s skipPaths) Skip(rel string, _ bool) bool {
	for _, skipped := range s {
		if rel == skipped || strings.HasPrefix(rel, skipped+"/") {
			return true
		}
	}
	return false
}

// TestGenerateGitGeneratorSkipsWorktreePaths pins the working-tree filter:
// the Git generator does not see a path that Options.Worktree skips, as an
// Argo CD checkout never holds a path that Git ignores and does not track.
func TestGenerateGitGeneratorSkipsWorktreePaths(t *testing.T) {
	root := writeGitParityTree(t, "apps/alpha/config.yaml", "apps/ignored/config.yaml", "apps/beta/config.yaml", "apps/beta/ignored.yaml")
	options := Options{Worktree: skipPaths{"apps/ignored", "apps/beta/ignored.yaml"}}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("apps"), options)
	if err != nil || len(diags) != 0 {
		t.Fatalf("error = %v, diagnostics = %#v", err, diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"apps-alpha-config-yaml", "apps-beta-config-yaml"}) {
		t.Fatalf("generated names = %q", got)
	}
	options.NewGitFileGlobbing = true
	apps, _, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("apps/**/*.yaml"), options)
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"apps-alpha-config-yaml", "apps-beta-config-yaml"}) {
		t.Fatalf("generated names with new globbing = %q", got)
	}
	apps, _, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), options)
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dir-apps-alpha", "dir-apps-beta"}) {
		t.Fatalf("generated directory names = %q", got)
	}
}

// TestGenerateGitGeneratorLooksUpTheAppProject pins
// GitGenerator.resolveSourceIntegrity (applicationset/generators/git.go:93-111
// at v3.6.1): the Git generator gets the AppProject that the
// ApplicationSet's spec.template.spec.project names before it lists
// anything. An empty name never exists, so the ApplicationSet fails. A
// templated name skips the lookup. Any other name is recorded on every
// generated Application, for discovery to check.
func TestGenerateGitGeneratorLooksUpTheAppProject(t *testing.T) {
	root := writeGitParityTree(t, "apps/alpha/x.yaml")
	appSet := func(project, generatorProject string) []byte {
		return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: projects
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - path: {path: list, basename: list}
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - git:
              directories:
                - path: apps/*
              template:
                metadata: {}
                spec:
                  project: '` + generatorProject + `'
                  destination: {}
  template:
    metadata:
      name: '{{ .path.basename }}-{{ .env | default "x" }}'
    spec:
      project: '` + project + `'
`)
	}
	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", appSet("", "team"))
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, `error getting project : appprojects.argoproj.io "" not found`) {
		t.Fatalf("apps = %q, diagnostics = %#v, want the empty project lookup to fail the ApplicationSet", generatedNames(apps), diags)
	}

	apps, diags, err = GenerateFromYAML(root, "app-set.yaml", appSet("{{ .env }}", "team"))
	if err != nil || len(diags) != 0 {
		t.Fatalf("error = %v, diagnostics = %#v", err, diags)
	}
	for _, app := range apps {
		if app.GitGeneratorProject != nil {
			t.Fatalf("%s: GitGeneratorProject = %q, want no lookup for a templated project", app.Application.Name, *app.GitGeneratorProject)
		}
	}

	apps, diags, err = GenerateFromYAML(root, "app-set.yaml", appSet("platform", "team"))
	if err != nil || len(diags) != 0 {
		t.Fatalf("error = %v, diagnostics = %#v", err, diags)
	}
	if len(apps) != 2 {
		t.Fatalf("generated names = %q, want the list and the matrix Application", generatedNames(apps))
	}
	for _, app := range apps {
		// The lookup uses the ApplicationSet's template, not the Git
		// generator's, and covers every Application of the ApplicationSet.
		if app.GitGeneratorProject == nil || *app.GitGeneratorProject != "platform" {
			t.Fatalf("%s: GitGeneratorProject = %v, want platform", app.Application.Name, app.GitGeneratorProject)
		}
	}
}

// TestDoublestarParsesLikeArgoCD pins the doublestar release that Argo CD
// v3.6.1 requires (v4.10.0). It refuses "{c,[a,b]}.yaml" with a syntax
// error, which fails the Git files generator with new globbing and the OCI
// files generator. Later releases accept it and match a.yaml and b.yaml.
func TestDoublestarParsesLikeArgoCD(t *testing.T) {
	root := writeGitParityTree(t, "a.yaml", "b.yaml", "c.yaml")
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("{c,[a,b]}.yaml"), Options{NewGitFileGlobbing: true})
	if err != nil || len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "syntax error in pattern") {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want a syntax error generator failure", generatedNames(apps), diags, err)
	}
	_, _, err = ociParamSetsJSON(t, root, `{"files":[{"path":"{c,[a,b]}.yaml"}]}`, true)
	if _, ok := errors.AsType[*generatorParamsError](err); !ok || !strings.Contains(err.Error(), "syntax error in pattern") {
		t.Fatalf("oci error = %v, want a syntax error generator failure", err)
	}
}

// TestGenerateGitFilesAttrMagicFailsOnlyTheApplicationSet pins a pathspec
// that git accepts but drydock does not evaluate: the attr magic makes the
// ApplicationSet unsupported, as an unsupported generator does, and does not
// fail the run. git lists files normally, so the warning does not say that
// Argo CD fails. After a generator fails the ApplicationSet,
// drydock evaluates no later generator, so a later drydock-side error, here
// an OCI acquisition that fails, cannot fail the run either.
func TestGenerateGitFilesAttrMagicFailsOnlyTheApplicationSet(t *testing.T) {
	root := writeGitParityTree(t, "params/tracked.yaml")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: attr
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: ':(attr:foo)params'
    - oci:
        repoURL: oci://registry.example.invalid/params
        revision: v1
        files:
          - path: '*.yaml'
  template:
    metadata:
      name: 'attr-{{ .path.basename }}'
    spec:
      project: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", data, Options{OCI: &fakeOCIFetcher{err: errors.New("registry unreachable")}})
	if !errors.Is(err, ErrUnsupportedGenerator) {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v, want the ApplicationSet unsupported alone", err)
	}
	if len(apps) != 0 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.unsupported-generator" ||
		!strings.Contains(diags[0].Message, "attr pathspec magic") {
		t.Fatalf("apps = %q, diagnostics = %#v, want one appset.unsupported-generator warning for the attr magic", generatedNames(apps), diags)
	}
}

// TestGenerateGitFilesSymlinkToIgnoredTargetIsMissing pins a committed
// symlinked param file whose target Git ignores and does not track. No
// checkout holds the target, so the repo-server read fails, and the
// ApplicationSet with it.
func TestGenerateGitFilesSymlinkToIgnoredTargetIsMissing(t *testing.T) {
	root := writeGitParityTree(t, "params/tracked.yaml", "local/dev.yaml")
	if err := os.Symlink("../local/dev.yaml", filepath.Join(root, "params", "linked.yaml")); err != nil {
		t.Fatal(err)
	}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("params/*.yaml"), Options{Worktree: skipPaths{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "params/linked.yaml") || !strings.Contains(diags[0].Message, "no such file or directory") {
		t.Fatalf("apps = %q, diagnostics = %#v, want a missing-target generator failure", generatedNames(apps), diags)
	}
}

// TestGenerateGitFilesListsGitlinkAsOneEntry pins a submodule or nested
// repository: `git ls-files` lists its directory as one gitlink entry and
// nothing in it, and the repo-server fails to read that directory. The
// directory generator still walks into it, as the repo-server walk does.
func TestGenerateGitFilesListsGitlinkAsOneEntry(t *testing.T) {
	root := writeGitParityTree(t, "params/top.yaml", "params/lib/p.yaml", "params/lib/.git")
	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet("params/*.yaml"))
	if err != nil || len(diags) != 0 {
		t.Fatalf("error = %v, diagnostics = %#v", err, diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"params-top-yaml"}) {
		t.Fatalf("generated names = %q, want params/top.yaml only", got)
	}
	apps, diags, err = GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet("params"))
	if err != nil || len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "read params/lib: is a directory") {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want an is a directory failure for the gitlink", generatedNames(apps), diags, err)
	}
	apps, _, err = GenerateFromYAML(root, "app-set.yaml", gitDirectoriesAppSet("params/*"))
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dir-params-lib"}) {
		t.Fatalf("generated directory names = %q, want params/lib", got)
	}
}

// TestGenerateGitFilesResolvesSymlinksLikeTheKernel pins symlink targets
// that os.ReadFile refuses: a path through a regular file fails with
// ENOTDIR, and a path that the checkout does not hold, such as a .git
// entry below the top level, ".GIT", or a name in another letter case,
// fails with ENOENT, also on a file system that ignores case. Each fails
// the ApplicationSet. A target in the top-level .git directory, also when a
// ".." leaves .git again, or outside the repository, is never read, so the
// ApplicationSet is unsupported. A chain of symlinks is followed.
func TestGenerateGitFilesResolvesSymlinksLikeTheKernel(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   string
	}{
		{"../targets/t.yaml/", "not a directory"},
		{"../targets/t.yaml/.", "not a directory"},
		{"../targets/t.yaml/../t.yaml", "not a directory"},
		{"../.git/HEAD", "is a symlink into the .git directory"},
		{"../.git/../targets/t.yaml", "is a symlink into the .git directory"},
		{"../.git", "is a directory"},
		{"../targets/.git/HEAD", "no such file or directory"},
		{"../.GIT/HEAD", "no such file or directory"},
		{"../../outside.yaml", "a target outside the repository"},
		{"/etc/hostname", "a target outside the repository"},
		{"../targets/T.yaml", "no such file or directory"},
		{"../l1/l2/x.yaml", ""},
	} {
		t.Run(tc.target, func(t *testing.T) {
			root := writeGitParityTree(t, "targets/t.yaml", "d2/x.yaml", ".git/HEAD")
			for link, target := range map[string]string{"l1": "d1", "d1/l2": "../d2", "params/link.yaml": tc.target} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
					t.Fatal(err)
				}
			}
			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet("params/link.yaml"))
			if err != nil && !errors.Is(err, ErrUnsupportedGenerator) {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(apps) != 1 || len(diags) != 0 {
					t.Fatalf("apps = %q, diagnostics = %#v, want the chained target read", generatedNames(apps), diags)
				}
				return
			}
			if len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, tc.want) {
				t.Fatalf("apps = %q, diagnostics = %#v, want %q", generatedNames(apps), diags, tc.want)
			}
		})
	}
}

// indexGitlinks is a WorktreeFilter whose index lists gitlinks.
type indexGitlinks []string

func (indexGitlinks) Skip(string, bool) bool { return false }
func (g indexGitlinks) Gitlinks() []string   { return g }

// TestGenerateGitGeneratorSeesIndexGitlinks pins gitlinks (submodules) that
// only the index or the commit lists, as a clone without submodule content
// holds them: the checkout holds each one as an empty directory. The Git
// directories generator lists it, `git ls-files` lists it as one entry,
// also for its name with a trailing slash, and the repo-server fails to
// read it. With .gitmodules, Argo CD checks out submodule content, which
// this working tree does not hold, so drydock warns and names the empty
// submodule.
//
//nolint:gocyclo // One table drives both matching modes.
func TestGenerateGitGeneratorSeesIndexGitlinks(t *testing.T) {
	root := writeGitParityTree(t, "params/a.yaml", "apps/one/cm.yaml")
	options := Options{Worktree: indexGitlinks{"params/sub", "apps/mod"}}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), options)
	if err != nil || len(diags) != 0 {
		t.Fatalf("error = %v, diagnostics = %#v", err, diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dir-apps-mod", "dir-apps-one"}) {
		t.Fatalf("generated directory names = %q, want apps/mod and apps/one", got)
	}
	for _, pattern := range []string{"params/*", "params/sub/", "params"} {
		for _, newGlobbing := range []bool{false, true} {
			if newGlobbing && pattern == "params/sub/" {
				continue
			}
			options.NewGitFileGlobbing = newGlobbing
			apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(pattern), options)
			if newGlobbing && pattern == "params" {
				// A doublestar glob of a directory matches it, which fails
				// for any directory.
				continue
			}
			if err != nil || len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "read params/sub: is a directory") {
				t.Fatalf("pattern %q new globbing %t: apps = %q, diagnostics = %#v, error = %v, want an is a directory failure for the gitlink", pattern, newGlobbing, generatedNames(apps), diags, err)
			}
		}
	}
	writeAppsetTestFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"sub\"]\n")
	_, diags, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), Options{Worktree: indexGitlinks{"apps/mod"}})
	if err != nil || len(diags) != 1 || !strings.Contains(diags[0].Message, `submodule "apps/mod" holds nothing in the working tree`) {
		t.Fatalf("error = %v, diagnostics = %#v, want the submodule content warning", err, diags)
	}
	writeAppsetTestFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"mod\"]\n\tpath = apps/mod\n\turl = ../mod.git\n\tupdate = none\n")
	_, diags, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), Options{Worktree: indexGitlinks{"apps/mod"}})
	if err != nil || len(diags) != 0 {
		t.Fatalf("update = none: error = %v, diagnostics = %#v, want no warning for a skipped submodule", err, diags)
	}
	_, diags, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("apps/*"), Options{Worktree: indexGitlinks{"apps/mod"}, SubmodulesDisabled: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("submodules disabled: error = %v, diagnostics = %#v, want no warning", err, diags)
	}
}

// TestGenerateGitFilesNewGlobbingStatsThroughSymlinks pins how the glob
// that Argo CD runs with new globbing treats a symlink it cannot follow: a
// literal pattern part stats through it, so a dangling symlink does not
// match and fails nothing, while a wildcard part lists the symlink, and
// reading it fails the ApplicationSet (util/git/client.go:740). The same
// holds for a chain, a loop, a target that Git ignores, and a path through
// a file.
func TestGenerateGitFilesNewGlobbingStatsThroughSymlinks(t *testing.T) {
	root := writeGitParityTree(t, "x.yaml", "local/dev.yaml", "targets/t.yaml")
	for link, target := range map[string]string{
		"dangling.yaml": "nope.yaml",
		"chain.yaml":    "dangling.yaml",
		"loop.yaml":     "loop.yaml",
		"ignored.yaml":  "local/dev.yaml",
		"through.yaml":  "targets/t.yaml/x",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{NewGitFileGlobbing: true, Worktree: skipPaths{"local"}}
	for _, name := range []string{"dangling", "chain", "loop", "ignored", "through"} {
		for _, pattern := range []string{name + ".yaml", "{x," + name + "}.yaml"} {
			apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(pattern), options)
			want := []string{}
			if strings.HasPrefix(pattern, "{") {
				want = []string{"root-x-yaml"}
			}
			if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), want) {
				t.Fatalf("pattern %q: apps = %q, diagnostics = %#v, error = %v, want %q", pattern, generatedNames(apps), diags, err, want)
			}
		}
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(name+".y*"), options)
		if err != nil || len(apps) != 0 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != diagnostic.CodeAppSetGeneratorFailed {
			t.Fatalf("pattern %q: apps = %q, diagnostics = %#v, error = %v, want a generator failure", name+".y*", generatedNames(apps), diags, err)
		}
	}
}

// TestGenerateGitFilesNewGlobbingLooksUpLazily pins that new globbing looks
// up only the paths the pattern needs: two symlinks to "." do not slow a
// narrow pattern down, and a pattern that would visit their 2^40 paths
// makes only that ApplicationSet unsupported.
func TestGenerateGitFilesNewGlobbingLooksUpLazily(t *testing.T) {
	root := writeGitParityTree(t, "apps/a.yaml")
	for _, link := range []string{"l1", "l2"} {
		if err := os.Symlink(".", filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{NewGitFileGlobbing: true}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("apps/*.yaml"), options)
	if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), []string{"apps-a-yaml"}) {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want apps/a.yaml", generatedNames(apps), diags, err)
	}
	apps, diags, err = GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("**/*.yaml"), options)
	if !errors.Is(err, ErrUnsupportedGenerator) || len(apps) != 0 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.unsupported-generator" {
		t.Fatalf("apps = %d, diagnostics = %#v, error = %v, want an unsupported ApplicationSet", len(apps), diags, err)
	}
}

// TestGenerateGitFilesSourcePathsFollowSymlinks pins the paths that a
// param file read through symlinks owns: the listed path, every symlink
// followed, and the file read. Changed-only selection then sees a change
// of the target.
func TestGenerateGitFilesSourcePathsFollowSymlinks(t *testing.T) {
	root := writeGitParityTree(t, "targets/t.yaml", "real/one.yaml")
	for link, target := range map[string]string{"params/link.yaml": "../targets/t.yaml", "linkdir": "real"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		pattern     string
		newGlobbing bool
		want        []string
	}{
		{"params/link.yaml", false, []string{"params/link.yaml", "targets/t.yaml"}},
		{"linkdir/*.yaml", true, []string{"linkdir", "linkdir/one.yaml", "real/one.yaml"}},
	} {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(tc.pattern), Options{NewGitFileGlobbing: tc.newGlobbing})
		if err != nil || len(diags) != 0 || len(apps) != 1 {
			t.Fatalf("pattern %q: apps = %q, diagnostics = %#v, error = %v", tc.pattern, generatedNames(apps), diags, err)
		}
		if got := apps[0].SourcePaths; !slices.Equal(got, tc.want) {
			t.Fatalf("pattern %q: SourcePaths = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

// TestGenerateGitFilesSymlinkToFIFOFailsOnlyTheApplicationSet pins a
// tracked symlink whose target is a FIFO, which no checkout holds: the
// repo-server finds no file, and only the ApplicationSet fails.
func TestGenerateGitFilesSymlinkToFIFOFailsOnlyTheApplicationSet(t *testing.T) {
	root := writeGitParityTree(t, "params/a.yaml")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink("../pipe", filepath.Join(root, "params", "pipe.yaml")); err != nil {
		t.Fatal(err)
	}
	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", gitFilesAppSet("params/*.yaml"))
	if err != nil || len(apps) != 0 || len(diags) != 1 || !strings.Contains(diags[0].Message, "no such file or directory") {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want a no such file generator failure", generatedNames(apps), diags, err)
	}
}

// TestGenerateGitGeneratorHidesGitlinkContentWithoutGitmodules pins a
// gitlink whose content is on disk, in a repository without .gitmodules.
// Argo CD fetches submodule content only when .gitmodules exists
// (util/git/client.go:812-817) and then cleans the checkout, so the gitlink
// is an empty directory: no generator sees what is in it
// (TestGenerateGitGeneratorSeesSubmoduleContent covers .gitmodules).
func TestGenerateGitGeneratorHidesGitlinkContentWithoutGitmodules(t *testing.T) {
	root := writeGitParityTree(t, "params/a.yaml", "params/sub/x.yaml", "params/sub/inner/cm.yaml", "vendor/sub/inner/cm.yaml")
	if err := os.WriteFile(filepath.Join(root, "params", "sub", ".git"), []byte("gitdir: ../../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := Options{Worktree: indexGitlinks{"params/sub", "vendor/sub"}}
	// A pathspec "*" matches "/", and "**" of a doublestar glob matches
	// any number of directories.
	for newGlobbing, pattern := range map[bool]string{false: "params/*.yaml", true: "params/**/*.yaml"} {
		options.NewGitFileGlobbing = newGlobbing
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(pattern), options)
		if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), []string{"params-a-yaml"}) {
			t.Fatalf("new globbing %t: apps = %q, diagnostics = %#v, error = %v, want params/a.yaml only", newGlobbing, generatedNames(apps), diags, err)
		}
	}
	options.NewGitFileGlobbing = false
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("params/*", "params/*/*", "vendor/sub/*"), options)
	if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), []string{"dir-params-sub"}) {
		t.Fatalf("apps = %q, diagnostics = %#v, error = %v, want params/sub only", generatedNames(apps), diags, err)
	}
}

// TestGenerateGitGeneratorSeesSubmoduleContent pins a gitlink whose
// content is on disk, in a repository with .gitmodules: the generators see
// the submodule content, as Argo CD's do after `git submodule update
// --init --recursive`, without a warning. With
// applicationsetcontroller.enable.git.submodule off, the gitlinks are empty
// again. ls-files still lists a gitlink as one entry, and the new globbing
// walks the submodule content.
func TestGenerateGitGeneratorSeesSubmoduleContent(t *testing.T) {
	root := writeGitParityTree(t, "params/a.yaml", "params/sub/x.yaml", "params/sub/inner/cm.yaml", "vendor/sub/inner/cm.yaml")
	writeAppsetTestFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"sub\"]\n")
	options := Options{Worktree: indexGitlinks{"params/sub", "vendor/sub"}}
	directories := gitDirectoriesAppSet("params/*", "params/*/*", "vendor/sub/*")
	cases := []struct {
		name     string
		options  Options
		appSet   []byte
		expected []string
	}{
		{"directories", options, directories, []string{"dir-params-sub", "dir-params-sub-inner", "dir-vendor-sub-inner"}},
		{"submodules disabled", Options{Worktree: options.Worktree, SubmodulesDisabled: true}, directories, []string{"dir-params-sub"}},
		{"ls-files", options, gitFilesAppSet("params/*.yaml"), []string{"params-a-yaml"}},
		{"new globbing", Options{Worktree: options.Worktree, NewGitFileGlobbing: true}, gitFilesAppSet("params/**/*.yaml"), []string{"params-a-yaml", "params-sub-inner-cm-yaml", "params-sub-x-yaml"}},
	}
	for _, tc := range cases {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", tc.appSet, tc.options)
		if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), tc.expected) {
			t.Fatalf("%s: apps = %q, diagnostics = %#v, error = %v, want %q", tc.name, generatedNames(apps), diags, err, tc.expected)
		}
	}
}

// TestGenerateGitFilesNewGlobbingThroughOutsideSymlinks pins a new-globbing
// pattern that reaches a symlink to a target outside the repository.
// Argo CD's LsFiles follows the symlink, drops every match whose real path
// is outside the checkout, and fails only when it cannot resolve a match
// (util/git/client.go:741-758). So when only literal pattern parts remain
// below the symlink, or the symlink itself matches, nothing matches. When a
// wildcard still has to match below it, Argo CD walks a tree that drydock
// does not read, which can even hold the checkout itself (a symlink to
// ".."), so the ApplicationSet is unsupported. drydock never reads the
// target.
func TestGenerateGitFilesNewGlobbingThroughOutsideSymlinks(t *testing.T) {
	for _, tc := range []struct {
		target      string
		pattern     string
		unsupported bool
	}{
		{"/etc", ".h/hostname", false},
		{"/etc", "*/hostname", false},
		{"/etc", "{a,.h}/hostname", false},
		{"/etc", "?h", false},
		{"/etc", ".h", false},
		{"..", ".h/*", true},
		{"/etc", "{a,.h}/*", true},
		{"/etc", "**/*.yaml", true},
		{"../b/d.yaml", "**/.h", true},
		{"/", ".h/*/hostname", true},
	} {
		root := writeGitParityTree(t, "a/x.yaml")
		if err := os.Symlink(tc.target, filepath.Join(root, ".h")); err != nil {
			t.Fatal(err)
		}
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(tc.pattern), Options{NewGitFileGlobbing: true})
		if tc.unsupported {
			if !errors.Is(err, ErrUnsupportedGenerator) || len(apps) != 0 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.unsupported-generator" {
				t.Fatalf(".h -> %s, pattern %q: apps = %q, diagnostics = %#v, error = %v, want an unsupported ApplicationSet", tc.target, tc.pattern, generatedNames(apps), diags, err)
			}
			continue
		}
		if err != nil || len(diags) != 0 || len(apps) != 0 {
			t.Fatalf(".h -> %s, pattern %q: apps = %q, diagnostics = %#v, error = %v, want no match", tc.target, tc.pattern, generatedNames(apps), diags, err)
		}
	}
}

// TestGenerateGitFilesNewGlobbingThroughGitAndBack pins a symlink that
// enters the top-level .git directory and leaves it again, such as
// .git/../values.yaml. Argo CD reads the file it resolves to, but drydock
// never follows a path through .git, so the ApplicationSet is unsupported
// for a literal pattern and a wildcard, as with default globbing.
func TestGenerateGitFilesNewGlobbingThroughGitAndBack(t *testing.T) {
	root := writeGitParityTree(t, "values.yaml", ".git/HEAD")
	if err := os.Symlink(".git/../values.yaml", filepath.Join(root, "x.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pattern     string
		newGlobbing bool
	}{{"x.yaml", false}, {"x.yaml", true}, {"x.*", true}} {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(tc.pattern), Options{NewGitFileGlobbing: tc.newGlobbing})
		if !errors.Is(err, ErrUnsupportedGenerator) || len(apps) != 0 || len(diags) != 1 {
			t.Fatalf("pattern %q new globbing %t: apps = %q, diagnostics = %#v, error = %v, want an unsupported ApplicationSet", tc.pattern, tc.newGlobbing, generatedNames(apps), diags, err)
		}
	}
}

// TestGenerateGitFilesNewGlobbingMatchesExactNames pins that a literal
// pattern part matches the exact name, as on Linux, where the repo-server
// runs, also on a file system that ignores case. On a case-sensitive file
// system the old code passed too; the test can only fail on one that
// ignores case, such as the default macOS file system.
func TestGenerateGitFilesNewGlobbingMatchesExactNames(t *testing.T) {
	root := writeGitParityTree(t, "params/a.yaml")
	for _, pattern := range []string{"params/A.yaml", "Params/a.yaml", "PARAMS/*.yaml"} {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(pattern), Options{NewGitFileGlobbing: true})
		if err != nil || len(diags) != 0 || len(apps) != 0 {
			t.Fatalf("pattern %q: apps = %q, diagnostics = %#v, error = %v, want no match", pattern, generatedNames(apps), diags, err)
		}
	}
}

// TestGitlinkPathsFromTheIndexAreChecked pins the gitlink names that
// drydock accepts from the index: the ones a commit can hold, so never an
// absolute name, one with ".." or a .git part, or one that is not clean.
// A trailing space is a legal name and stays.
func TestGitlinkPathsFromTheIndexAreChecked(t *testing.T) {
	for name, want := range map[string]bool{
		"params/sub":       true,
		"params/sub" + " ": true, // A trailing space is legal.
		"/x":               false,
		"/":                false,
		"a/../b":           false,
		"../b":             false,
		"a/.git/b":         false,
		"a/.GIT":           false,
		"a//b":             false,
		"a/b/":             false,
		"":                 false,
		".":                false,
	} {
		if got := validGitlinkPath(name); got != want {
			t.Fatalf("validGitlinkPath(%q) = %t, want %t", name, got, want)
		}
	}
	root := writeGitParityTree(t, "params/a.yaml")
	tree := newGitRepositoryTree(root, indexGitlinks{"/x", "a/../b", "params/sub "})
	// An absolute name used to loop forever (path.Dir("/") is "/"), so the
	// scan runs under a deadline.
	type scanResult struct {
		dirs []string
		err  error
	}
	done := make(chan scanResult, 1)
	go func() {
		dirs, err := tree.directories(false)
		done <- scanResult{dirs, err}
	}()
	var dirs []string
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		dirs = result.dirs
	case <-time.After(5 * time.Second):
		t.Fatal("directories() did not return within 5s")
	}
	if !slices.Equal(dirs, []string{"params", "params/sub "}) {
		t.Fatalf("directories = %q, want params and the gitlink with a trailing space", dirs)
	}
}

// TestGitlinkListerIsTrusted pins that a directory with a stray .git entry
// is no gitlink when the index or the commit lists the gitlinks: the files
// there stay files of the checkout. Only without a lister is a directory
// with its own .git taken as a gitlink.
func TestGitlinkListerIsTrusted(t *testing.T) {
	root := writeGitParityTree(t, "apps/foo/x.yaml", "apps/foo/.git")
	for _, newGlobbing := range []bool{false, true} {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("apps/*/x.yaml"), Options{Worktree: indexGitlinks{}, NewGitFileGlobbing: newGlobbing})
		if err != nil || len(diags) != 0 || !slices.Equal(generatedNames(apps), []string{"apps-foo-x-yaml"}) {
			t.Fatalf("new globbing %t: apps = %q, diagnostics = %#v, error = %v, want apps/foo/x.yaml", newGlobbing, generatedNames(apps), diags, err)
		}
	}
	apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet("apps/*/x.yaml"), Options{NewGitFileGlobbing: true})
	if err != nil || len(diags) != 0 || len(apps) != 0 {
		t.Fatalf("without a lister: apps = %q, diagnostics = %#v, error = %v, want apps/foo taken as an empty gitlink", generatedNames(apps), diags, err)
	}
}

// TestGitmodulesMustBeARegularFile pins that only a regular .gitmodules file
// makes drydock keep the local content of a gitlink. A directory or a
// symlink named .gitmodules is no submodule configuration.
func TestGitmodulesMustBeARegularFile(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		root := writeGitParityTree(t, "params/sub/inner/x.yaml", "real-gitmodules")
		switch kind {
		case "file":
			writeAppsetTestFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"sub\"]\n")
		case "directory":
			writeAppsetTestFile(t, filepath.Join(root, ".gitmodules", "x"), "x\n")
		case "symlink":
			if err := os.Symlink("real-gitmodules", filepath.Join(root, ".gitmodules")); err != nil {
				t.Fatal(err)
			}
		}
		apps, _, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitDirectoriesAppSet("params/sub/*"), Options{Worktree: indexGitlinks{"params/sub"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{}
		if kind == "file" {
			want = []string{"dir-params-sub-inner"}
		}
		if got := generatedNames(apps); !slices.Equal(got, want) {
			t.Fatalf(".gitmodules %s: generated names = %q, want %q", kind, got, want)
		}
	}
}

// TestGitLookupsComposeUnicodeOnMacOS pins that a name the macOS file
// system holds decomposed (NFD) matches its composed (NFC) spelling, which
// Git commits and the Linux checkout holds, for a symlink target and a
// literal new-globbing pattern part. Elsewhere the two spellings are
// different names, so the test runs on macOS only.
func TestGitLookupsComposeUnicodeOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS composes names")
	}
	nfc := "caf\u00e9.yaml"
	nfd := "cafe\u0301.yaml"
	root := writeGitParityTree(t, "real/"+nfd)
	if err := os.MkdirAll(filepath.Join(root, "params"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real/"+nfc, filepath.Join(root, "params", "link.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pattern     string
		newGlobbing bool
	}{{"params/link.yaml", false}, {"real/" + nfc, true}} {
		apps, diags, err := GenerateFromYAMLWithOptions(root, "app-set.yaml", gitFilesAppSet(tc.pattern), Options{NewGitFileGlobbing: tc.newGlobbing})
		if err != nil || len(diags) != 0 || len(apps) != 1 {
			t.Fatalf("pattern %q: apps = %q, diagnostics = %#v, error = %v, want one Application", tc.pattern, generatedNames(apps), diags, err)
		}
	}
}
