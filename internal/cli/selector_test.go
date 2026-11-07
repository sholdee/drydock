package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The -l/--selector tests use two teams: team=a holds alpha, team=b holds
// beta. A selector that matches neither must print the selector.no-match
// warning instead of an empty result that looks like an empty repository or
// like no change.

const selectorNoMatchWarning = `warning selector.no-match: label selector "team=none" matched no Application`

func TestBuildAppsSelectorBuildsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, root, "beta", "b", "v1")

	result := runCLI(t, "build", "apps", "--path", root, "-l", "team=a")
	assertStdoutContainsAll(t, result, "name: alpha")
	assertStdoutExcludesAll(t, result, "beta")
	assertStderrEmpty(t, result)

	result = runCLI(t, "build", "apps", "--path", root, "-l", "team=none", "-o", "json")
	assertStdoutExcludesAll(t, result, "alpha", "beta")
	assertStderrContains(t, result, selectorNoMatchWarning)
}

func TestTestAppsSelectorTestsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, root, "beta", "b", "v1")

	result := runCLI(t, "test", "apps", "--path", root, "-l", "team=a")
	if got, want := result.Stdout, "PASS argocd/alpha\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	assertStderrEmpty(t, result)

	result = runCLI(t, "test", "apps", "--path", root, "-l", "team=none")
	if got, want := result.Stdout, "No Applications matched the selector.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	assertStderrContains(t, result, selectorNoMatchWarning)
}

func TestGetAppsSelectorWarnsWhenNoApplicationMatches(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")

	result := runCLI(t, "get", "apps", "--path", root, "-o", "name", "-l", "team=none")
	if result.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.Stdout)
	}
	assertStderrContains(t, result, selectorNoMatchWarning)
}

func TestGetImagesSelectorListsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, root, "beta", "b", "v1")

	result := runCLI(t, "get", "images", "--path", root, "-o", "name", "-l", "team=a")
	if got, want := result.Stdout, "example/alpha:v1\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	assertStderrEmpty(t, result)

	result = runCLI(t, "get", "images", "--path", root, "-o", "name", "-l", "team=none")
	if result.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.Stdout)
	}
	assertStderrContains(t, result, selectorNoMatchWarning)
}

func TestDiffAppsSelectorDiffsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []struct{ root, value string }{{left, "v1"}, {right, "v2"}} {
		writeSelectorAppForCLI(t, side.root, "alpha", "a", side.value)
		writeSelectorAppForCLI(t, side.root, "beta", "b", side.value)
	}

	for _, changedOnly := range []string{"--changed-only=true", "--changed-only=false"} {
		t.Run(changedOnly, func(t *testing.T) {
			result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, changedOnly, "--exit-code=false", "-l", "team=a")
			assertStdoutContainsAll(t, result, "Application: argocd/alpha", "-  value: v1", "+  value: v2")
			assertStdoutExcludesAll(t, result, "beta")
			assertStderrEmpty(t, result)

			// With the default --exit-code, a diff with no selected
			// Application has no change and exits 0.
			result = runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, changedOnly, "-l", "team=none")
			if result.Stdout != "" {
				t.Fatalf("stdout = %q, want empty", result.Stdout)
			}
			assertStderrContains(t, result, selectorNoMatchWarning)
		})
	}
}

func TestDiffAppsSelectorKeepsAddedAndRemovedApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSelectorAppForCLI(t, left, "removed", "a", "v1")
	writeSelectorAppForCLI(t, left, "beta", "b", "v1")
	writeSelectorAppForCLI(t, right, "added", "a", "v1")
	writeSelectorAppForCLI(t, right, "beta", "b", "v2")

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false", "-l", "team=a")
	assertStdoutContainsAll(t, result,
		"Application: argocd/removed", "-  name: removed",
		"Application: argocd/added", "+  name: added",
	)
	assertStdoutExcludesAll(t, result, "beta")
	assertStderrEmpty(t, result)
}

// An Application whose labels change between the sides is diffed on both
// sides when either side matches, so its diff shows the change and not a
// whole added or removed Application.
func TestDiffAppsSelectorMatchesLabelsOnEitherSide(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSelectorAppForCLI(t, left, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, right, "alpha", "b", "v2")
	writeSelectorAppForCLI(t, left, "gamma", "c", "v1")
	writeSelectorAppForCLI(t, right, "gamma", "c", "v2")

	for _, selector := range []string{"team=a", "team=b"} {
		t.Run(selector, func(t *testing.T) {
			result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false", "-l", selector)
			assertStdoutContainsAll(t, result, "Application: argocd/alpha", "-  value: v1", "+  value: v2")
			assertStdoutExcludesAll(t, result, "-  name: alpha", "+  name: alpha", "gamma")
			assertStderrEmpty(t, result)
		})
	}
}

// With changed-only, an Application is diffed only when it is selected and
// changed. beta is selected but unchanged, and gamma is changed but not
// selected. Both fail to render, so the diff fails if either one renders.
func TestDiffAppsSelectorWithChangedOnlyNeedsSelectedAndChanged(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSelectorAppForCLI(t, left, "alpha", "a", "v1")
	writeBrokenAppForCLI(t, left, "beta", "a")
	writeSelectorAppForCLI(t, left, "gamma", "b", "v1")
	writeSelectorAppForCLI(t, right, "alpha", "a", "v2")
	writeBrokenAppForCLI(t, right, "beta", "a")
	writeBrokenAppForCLI(t, right, "gamma", "b")

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false", "-l", "team=a")
	assertStdoutContainsAll(t, result, "Application: argocd/alpha", "+  value: v2")
	assertStdoutExcludesAll(t, result, "beta", "gamma")
	assertStderrEmpty(t, result)

	// The fixture is sound: without changed-only, the selected beta
	// renders and fails, and without the selector, the changed gamma does.
	result, err := runCLIAllowError(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false", "--changed-only=false", "-l", "team=a")
	if err == nil {
		t.Fatalf("full diff error = nil, want the render failure of argocd/beta\nstdout:\n%s", result.Stdout)
	}
	assertStderrContains(t, result, "Application argocd/beta failed to render")
	result, err = runCLIAllowError(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false")
	if err == nil {
		t.Fatalf("unselected diff error = nil, want the render failure of argocd/gamma\nstdout:\n%s", result.Stdout)
	}
	assertStderrContains(t, result, "Application argocd/gamma failed to render")
}

// A child Application that only the render of its parent finds gets its
// labels from that render, and -l selects on them. The static copies of the
// children are hidden with --discover-ignore, as in the reviewer fixture.
func TestSelectorMatchesLabelsOfRenderedChildApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeAppOfAppsForCLI(t, left, "v1")
	writeAppOfAppsForCLI(t, right, "v2")

	result := runCLI(t, "get", "apps", "--path", right, "--discover-ignore", "children/**", "-o", "name", "-l", "team=a")
	if got, want := result.Stdout, "argocd/child1\nargocd/solo\n"; got != want {
		t.Fatalf("get apps stdout = %q, want %q", got, want)
	}

	result = runCLI(t, "build", "apps", "--path", right, "--discover-ignore", "children/**", "-l", "team=a")
	assertStdoutContainsAll(t, result, "name: child1", "name: solo")
	assertStdoutExcludesAll(t, result, "child2", "kind: Application")

	result = runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--discover-ignore", "children/**", "--exit-code=false", "-l", "team=a")
	assertStdoutContainsAll(t, result, "Application: argocd/child1", "-  value: v1", "+  value: v2")
	assertStdoutExcludesAll(t, result, "child2", "argocd/root")

	result = runCLI(t, "diag", "--path", right, "--discover-ignore", "children/**", "--render", "-l", "team=b")
	if got, want := result.Stdout, "No diagnostics found.\n"; got != want {
		t.Fatalf("diag --render stdout = %q, want %q\nstderr:\n%s", got, want, result.Stderr)
	}
	// Static diag cannot see child2, so its warning says that --render
	// finds the rendered children.
	result = runCLI(t, "diag", "--path", right, "--discover-ignore", "children/**", "-l", "team=b")
	assertStderrContains(t, result, `warning selector.no-match: label selector "team=b" matched no Application; static discovery does not see child Applications that only rendering finds, and --render includes them`)
}

// selector.no-match is strict-exempt: --strict does not fail a run whose
// selector matched nothing.
func TestSelectorNoMatchDoesNotFailStrict(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSelectorAppForCLI(t, left, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, right, "alpha", "a", "v2")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "diff apps", args: []string{"diff", "apps", "--path-orig", left, "--path", right, "--changed-only=false"}},
		{name: "diff apps changed-only", args: []string{"diff", "apps", "--path-orig", left, "--path", right}},
		{name: "test apps", args: []string{"test", "apps", "--path", right}},
		{name: "diag", args: []string{"diag", "--path", right}},
		{name: "diag render", args: []string{"diag", "--path", right, "--render"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runCLI(t, append(tc.args, "--strict", "-l", "team=none")...)
			assertStderrContains(t, result, selectorNoMatchWarning)
		})
	}
}

func TestDiffImagesSelectorDiffsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []struct{ root, value string }{{left, "v1"}, {right, "v2"}} {
		writeSelectorAppForCLI(t, side.root, "alpha", "a", side.value)
		writeSelectorAppForCLI(t, side.root, "beta", "b", side.value)
	}

	result := runCLI(t, "diff", "images", "--path-orig", left, "--path", right, "--exit-code=false", "-l", "team=a")
	if got, want := result.Stdout, "- example/alpha:v1\n+ example/alpha:v2\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	assertStderrEmpty(t, result)

	result = runCLI(t, "diff", "images", "--path-orig", left, "--path", right, "-l", "team=none")
	if result.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.Stdout)
	}
	assertStderrContains(t, result, selectorNoMatchWarning)
}

func TestDiagSelectorReportsOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")
	writeBrokenAppForCLI(t, root, "beta", "b")

	// beta fails to render, so a render-backed diag of every Application
	// reports it. With -l team=a, diag does not render beta.
	result := runCLI(t, "diag", "--path", root, "--render", "-l", "team=a")
	if got, want := result.Stdout, "No diagnostics found.\n"; got != want {
		t.Fatalf("stdout = %q, want %q\nstderr:\n%s", got, want, result.Stderr)
	}
	assertStderrEmpty(t, result)

	result, err := runCLIAllowError(t, "diag", "--path", root, "--render", "-l", "team=b")
	if err == nil {
		t.Fatalf("diag -l team=b error = nil, want the render failure of argocd/beta")
	}
	assertStderrContains(t, result, "error render.failed: Application argocd/beta failed to render")

	for _, mode := range [][]string{{}, {"--render"}} {
		args := append([]string{"diag", "--path", root, "-l", "team=none"}, mode...)
		result = runCLI(t, args...)
		if result.Stdout != "" {
			t.Fatalf("diag %v stdout = %q, want empty", mode, result.Stdout)
		}
		assertStderrContains(t, result, selectorNoMatchWarning)
	}
}

// diag without --render validates only the selected Applications against
// their AppProjects.
func TestDiagStaticSelectorValidatesOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "projects", "default.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: argocd
spec:
  sourceRepos:
  - https://github.com/example/other
  destinations:
  - name: in-cluster
    namespace: '*'
`)
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, root, "beta", "b", "v1")

	result := runCLI(t, "diag", "--path", root, "-o", "json", "-l", "team=a")
	if !strings.Contains(result.Stdout, "argocd/alpha") || strings.Contains(result.Stdout, "argocd/beta") {
		t.Fatalf("stdout = %s, want project diagnostics for argocd/alpha only", result.Stdout)
	}
}

func TestPluginPolicySelectorWarnsWhenNoApplicationMatches(t *testing.T) {
	root := t.TempDir()
	writeSelectorAppForCLI(t, root, "alpha", "a", "v1")

	for _, command := range [][]string{{"plugin-policy", "init"}, {"plugin-policy", "doctor"}} {
		result := runCLI(t, append(command, "--path", root, "-l", "team=none")...)
		assertStderrContains(t, result, selectorNoMatchWarning)
	}
}

// A command that names one Application has nothing for a selector to
// choose. It still accepts -l, hidden from help, ignores it, and prints a
// deprecation notice. Cobra prints the notice with OutOrStderr: stdout here,
// where the test sets the output writer, and stderr in the drydock binary.
func TestSingleApplicationCommandsIgnoreDeprecatedSelector(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSelectorAppForCLI(t, left, "alpha", "a", "v1")
	writeSelectorAppForCLI(t, right, "alpha", "a", "v2")
	const notice = "Flag --selector has been deprecated, -l does not apply to a single Application and is ignored"

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"build", "app", "alpha", "--path", right}, want: "name: alpha"},
		{args: []string{"test", "app", "alpha", "--path", right}, want: "PASS argocd/alpha"},
		{args: []string{"diff", "app", "alpha", "--path-orig", left, "--path", right, "--exit-code=false"}, want: "+  value: v2"},
	} {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			for _, flag := range []string{"-l", "--selector"} {
				// team=b does not match alpha: the selector is ignored.
				result := runCLI(t, append(tc.args, flag, "team=b")...)
				if output := result.Stdout + result.Stderr; !strings.Contains(output, notice) || !strings.Contains(output, tc.want) {
					t.Fatalf("%s output = %q, want %q and the notice %q", flag, output, tc.want, notice)
				}
			}
			cmd := findCLICommand(t, tc.args[:2]...)
			if selector := cmd.Flags().Lookup("selector"); selector == nil || !selector.Hidden {
				t.Fatalf("%s --selector = %#v, want a hidden flag", cmd.CommandPath(), selector)
			}
		})
	}
}

func TestFleetCommandsRegisterSelector(t *testing.T) {
	for _, command := range [][]string{
		{"get", "apps"}, {"get", "images"}, {"build", "apps"}, {"test", "apps"},
		{"diff", "apps"}, {"diff", "images"}, {"diag"},
		{"plugin-policy", "init"}, {"plugin-policy", "doctor"},
	} {
		cmd := findCLICommand(t, command...)
		if flag := cmd.Flags().ShorthandLookup("l"); flag == nil || flag.Name != "selector" {
			t.Fatalf("%s does not register -l/--selector", cmd.CommandPath())
		}
	}
}

// runCLIAllowError runs the command and returns its output and error.
func runCLIAllowError(t *testing.T, args ...string) (cliCommandResult, error) {
	t.Helper()
	cmd := NewRootCommand(VersionInfo{})
	var stdout, stderr bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return cliCommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func assertStderrContains(t *testing.T, result cliCommandResult, want string) {
	t.Helper()
	if !strings.Contains(result.Stderr, want) {
		t.Fatalf("stderr missing %q:\nstdout:\n%s\nstderr:\n%s", want, result.Stdout, result.Stderr)
	}
}

// writeSelectorAppForCLI writes Application appName with the label team and
// a ConfigMap and Deployment whose value and image tag are tag.
func writeSelectorAppForCLI(t *testing.T, root, appName, team, tag string) {
	t.Helper()
	writeSelectorApplicationForCLI(t, root, appName, team)
	writeCLITestFile(t, filepath.Join(root, "manifests", appName, "resources.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+appName+`
data:
  value: `+tag+`
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: `+appName+`
spec:
  selector:
    matchLabels:
      app: `+appName+`
  template:
    metadata:
      labels:
        app: `+appName+`
    spec:
      containers:
      - name: app
        image: example/`+appName+`:`+tag+`
`)
}

// writeAppOfAppsForCLI writes the reviewer's app-of-apps fixture: root
// (team=platform) renders children/, a Kustomization that adds team=a to
// child1 and team=b to child2, and solo has team=a. Each child ConfigMap
// value is tag.
func writeAppOfAppsForCLI(t *testing.T, root, tag string) {
	t.Helper()
	application := func(name, labels, path, namespace string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
  namespace: argocd
` + labels + `spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: ` + path + `
    targetRevision: HEAD
  destination:
    name: in-cluster
    namespace: ` + namespace + `
`
	}
	configMap := func(name string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\ndata:\n  value: " + tag + "\n"
	}
	writeCLITestFile(t, filepath.Join(root, "apps", "root.yaml"), application("root", "  labels:\n    team: platform\n", "children", "argocd"))
	writeCLITestFile(t, filepath.Join(root, "apps", "solo.yaml"), application("solo", "  labels:\n    team: a\n", "manifests/solo", "default"))
	writeCLITestFile(t, filepath.Join(root, "children", "child1.yml"), application("child1", "", "manifests/child1", "default"))
	writeCLITestFile(t, filepath.Join(root, "children", "child2.yml"), application("child2", "", "manifests/child2", "default"))
	writeCLITestFile(t, filepath.Join(root, "children", "kustomization.yaml"), `resources:
- child1.yml
- child2.yml
patches:
- target: {kind: Application, name: child1}
  patch: |-
    - op: add
      path: /metadata/labels
      value: {team: a}
- target: {kind: Application, name: child2}
  patch: |-
    - op: add
      path: /metadata/labels
      value: {team: b}
`)
	for _, name := range []string{"child1", "child2", "solo"} {
		writeCLITestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), configMap(name))
	}
}

// writeBrokenAppForCLI writes Application appName with the label team and a
// Kustomization that lists a missing resource, so it fails to render.
func writeBrokenAppForCLI(t *testing.T, root, appName, team string) {
	t.Helper()
	writeSelectorApplicationForCLI(t, root, appName, team)
	writeCLITestFile(t, filepath.Join(root, "manifests", appName, "kustomization.yaml"), "resources:\n- missing.yaml\n")
}

func writeSelectorApplicationForCLI(t *testing.T, root, appName, team string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
  labels:
    team: `+team+`
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
}
