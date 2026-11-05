package render

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func directoryConfigMap(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}

// writeDirectoryExtensionFixture lays out the files Argo CD v3.6.1's
// getPotentiallyValidManifestFile treats differently depending on
// disableExtensionFilter and the case-sensitive manifestFile regex.
func writeDirectoryExtensionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "plain.yaml"), directoryConfigMap("plain"))
	writeFile(t, filepath.Join(root, "apps", "short.yml"), directoryConfigMap("short"))
	writeFile(t, filepath.Join(root, "apps", "object.json"), `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"object"}}`)
	writeFile(t, filepath.Join(root, "apps", "secret.yaml.sealed"), directoryConfigMap("sealed"))
	writeFile(t, filepath.Join(root, "apps", "upper.YAML"), directoryConfigMap("upper-yaml"))
	writeFile(t, filepath.Join(root, "apps", "upper.JSON"), `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"upper-json"}}`)
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `{apiVersion: "v1", kind: "ConfigMap", metadata: {name: std.join("-", ["from", "jsonnet"])}}`)
	// An upper-case .JSONNET suffix is not Jsonnet to the repo-server: with the
	// filter disabled it is read as YAML, and this flow mapping is valid YAML.
	writeFile(t, filepath.Join(root, "apps", "upper.JSONNET"), `{apiVersion: v1, kind: ConfigMap, metadata: {name: upper-jsonnet-as-yaml}}`)
	writeFile(t, filepath.Join(root, "apps", "README.md"), "# Notes\n\nNot a manifest.\n")
	return root
}

func renderDirectoryExtensionFixture(t *testing.T, root string, opts RenderOptions) ([]string, []diagnostic.Diagnostic) {
	t.Helper()
	result, diags, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return directoryManifestNames(result), diags
}

func TestDirectoryRendererExtensionFilterIsCaseSensitive(t *testing.T) {
	root := writeDirectoryExtensionFixture(t)

	names, diags := renderDirectoryExtensionFixture(t, root, RenderOptions{})
	want := []string{"from-jsonnet", "object", "plain", "short"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v (upper-case extensions and custom extensions skipped)", names, want)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestDirectoryRendererDisableExtensionFilterReadsEveryFile(t *testing.T) {
	root := writeDirectoryExtensionFixture(t)

	names, diags := renderDirectoryExtensionFixture(t, root, RenderOptions{DirectoryDisableExtensionFilter: true})
	want := []string{"from-jsonnet", "object", "plain", "sealed", "short", "upper-json", "upper-jsonnet-as-yaml", "upper-yaml"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one extension-filter warning", diags)
	}
	diag := diags[0]
	if diag.Code != DirectoryExtensionFilterDisabledCode || diag.Severity != diagnostic.SeverityWarning || diag.Category != "directory" {
		t.Fatalf("diagnostic = %#v, want %s warning", diag, DirectoryExtensionFilterDisabledCode)
	}
	if !strings.Contains(diag.Message, "without include or exclude") {
		t.Fatalf("diagnostic message = %q", diag.Message)
	}
}

func TestDirectoryRendererDisableExtensionFilterUsesIncludeAndExclude(t *testing.T) {
	root := writeDirectoryExtensionFixture(t)

	names, diags := renderDirectoryExtensionFixture(t, root, RenderOptions{
		DirectoryDisableExtensionFilter: true,
		DirectoryInclude:                "{*.yaml.sealed,*.yaml}",
	})
	if want := []string{"plain", "sealed"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("include rendered names = %#v, want %#v", names, want)
	}
	if len(diags) != 0 {
		t.Fatalf("include diagnostics = %#v, want none", diags)
	}

	names, diags = renderDirectoryExtensionFixture(t, root, RenderOptions{
		DirectoryDisableExtensionFilter: true,
		DirectoryExclude:                "{*.JSON,*.JSONNET,*.YAML}",
	})
	if want := []string{"from-jsonnet", "object", "plain", "sealed", "short"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("exclude rendered names = %#v, want %#v", names, want)
	}
	if len(diags) != 0 {
		t.Fatalf("exclude diagnostics = %#v, want none", diags)
	}
}

func TestDirectoryRendererDisableExtensionFilterKeepsSkipMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "kept.sealed"), directoryConfigMap("kept"))
	writeFile(t, filepath.Join(root, "apps", "skipped.sealed"), "# +argocd:skip-file-rendering\n"+directoryConfigMap("skipped"))

	names, _ := renderDirectoryExtensionFixture(t, root, RenderOptions{DirectoryDisableExtensionFilter: true, DirectoryInclude: "*.sealed"})
	if want := []string{"kept"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}
}

func TestDirectoryRendererRejectsMultipleObjectsInJSONFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "multi.json"), `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"one"}}
{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"two"}}`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "only single objects are allowed in JSON files") {
		t.Fatalf("Render() error = %v, want single-object JSON error", err)
	}

	// Only a case-sensitive ".json" suffix selects the single-object rule;
	// any other file name is a YAML/JSON stream.
	writeFile(t, filepath.Join(root, "stream", "multi.JSON"), `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"one"}}
{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"two"}}`)
	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "stream"}, RenderOptions{DirectoryDisableExtensionFilter: true})
	if err != nil {
		t.Fatalf("Render() stream error = %v", err)
	}
	if got, want := directoryManifestNames(result), []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream rendered names = %#v, want %#v", got, want)
	}
}

func TestDirectoryRendererRejectsBOMPrefixedMultipleObjectsInJSONFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "multi.json"), "\xef\xbb\xbf"+`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"one"}}
{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"two"}}`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "only single objects are allowed in JSON files") {
		t.Fatalf("Render() error = %v, want single-object JSON error", err)
	}

	writeFile(t, filepath.Join(root, "single", "one.json"), "\xef\xbb\xbf"+`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"one"}}`)
	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "single"}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() single error = %v", err)
	}
	if got, want := directoryManifestNames(result), []string{"one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("single rendered names = %#v, want %#v", got, want)
	}
}

func TestDirectoryRendererRejectsJSONFileThatIsNotOneObject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty.json", content: "", want: "file is empty"},
		{name: "yaml.json", content: directoryConfigMap("yaml-in-json"), want: "invalid JSON at offset 1"},
		{name: "array.json", content: `[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a"}}]`, want: "JSON value is not an object"},
		{name: "truncated.json", content: `{"apiVersion":"v1","kind":"ConfigMap"`, want: "unexpected end of JSON input"},
		{name: "kindless.json", content: `{"apiVersion":"v1","schemaVersion":"secret-value"}`, want: "JSON object is missing kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", tc.name), tc.content)
			_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want JSON object error")
			}
			want := "apps/" + tc.name + ": decode JSON object failed: " + tc.want
			if err.Error() != want {
				t.Fatalf("Render() error = %q, want %q", err, want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("Render() error echoes file content: %q", err)
			}
		})
	}
}

// TestDirectoryRendererJSONFileFailsWithoutKind pins the Argo CD v3.6.1 path
// for a .json file whose object has no kind but passes the OCI image
// manifest test: the repo-server emits it and the controller fails the
// Application with "Object 'Kind' is missing".
// TestDirectoryRendererDecodeMatchesUpstream proves the outcome against the
// upstream code.
func TestDirectoryRendererJSONFileFailsWithoutKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		value   string
	}{
		{name: "package.json", content: `{"name":"web","version":"1.0.0","dependencies":{"vite":"^5.0.0"}}`, value: "JSON object"},
		{name: "tsconfig.json", content: `{"compilerOptions":{"strict":true},"include":["src"]}`, value: "JSON object"},
		{name: "empty-object.json", content: "{}\n", value: "JSON object"},
		{name: "oci.json", content: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`, value: "JSON object"},
		{name: "null.json", content: "null", value: "JSON value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", tc.name), tc.content)
			writeFile(t, filepath.Join(root, "apps", "cm.yaml"), directoryConfigMap("cm"))
			_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
			want := "apps/" + tc.name + ": " + tc.value + ` has no kind, so Argo CD fails the Application with "Object 'Kind' is missing"`
			if err == nil || err.Error() != want {
				t.Fatalf("Render() error = %v, want %q", err, want)
			}
		})
	}
}

// TestDirectoryRendererKindlessJSONReportsRepoServerFailureFirst pins the
// error order. Argo CD fails on a .json object without a kind only in the
// controller, after the repo-server has read every file, so a file that
// fails in the repo-server is the one reported.
func TestDirectoryRendererKindlessJSONReportsRepoServerFailureFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "a-package.json"), `{"name":"web"}`)
	writeFile(t, filepath.Join(root, "apps", "b-broken.json"), `{"apiVersion":"v1",`)
	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err == nil || !strings.HasPrefix(err.Error(), "apps/b-broken.json: decode JSON object failed") {
		t.Fatalf("Render() error = %v, want the b-broken.json decode failure", err)
	}

	writeFile(t, filepath.Join(root, "apps", "b-broken.json"), `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"fixed"}}`)
	writeFile(t, filepath.Join(root, "apps", "c-tsconfig.json"), `{"include":["src"]}`)
	_, _, err = (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err == nil || !strings.HasPrefix(err.Error(), "apps/a-package.json: JSON object has no kind") {
		t.Fatalf("Render() error = %v, want the first kind-less file, a-package.json", err)
	}
}

// TestDirectoryRendererKindlessJSONListRendersItems pins the List shape on
// the same path. GenerateManifests flattens an object whose items field is an
// array, whatever its kind, so the items render and the object itself is
// never emitted.
func TestDirectoryRendererKindlessJSONListRendersItems(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "list.json"), `{"apiVersion":"v1","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"item"}}]}`)
	writeFile(t, filepath.Join(root, "apps", "null-items.json"), `{"items":null}`)

	names, _ := renderDirectoryExtensionFixture(t, root, RenderOptions{})
	if want := []string{"item"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}
}

// TestDirectoryRendererDisableExtensionFilterDecodeFailures pins the
// repo-server's skip-or-fail rule for files that do not decode, which
// disableExtensionFilter exposes to arbitrary file names: a file fails only
// when it holds "apiVersion:", "kind:" and "metadata:".
func TestDirectoryRendererDisableExtensionFilterDecodeFailures(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "renovate.json5"), "{\n  // renovate\n  \"kind\": \"x\",\n  extends: [\"a\"],\n}\n")
	writeFile(t, filepath.Join(root, "apps", "cm.yaml.sealed"), directoryConfigMap("sealed"))

	names, _ := renderDirectoryExtensionFixture(t, root, RenderOptions{DirectoryDisableExtensionFilter: true, DirectoryInclude: "*"})
	if want := []string{"sealed"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v (json5 without apiVersion: skipped)", names, want)
	}

	writeFile(t, filepath.Join(root, "apps", "lib.libsonnet"), "local k = import 'k.libsonnet';\n{\n  configMap(name):: {\n    apiVersion: 'v1',\n    kind: 'ConfigMap',\n    metadata: { name: name },\n  },\n}\n")
	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{DirectoryDisableExtensionFilter: true, DirectoryInclude: "*"})
	if err == nil || !strings.Contains(err.Error(), "apps/lib.libsonnet document 0: decode YAML document failed") {
		t.Fatalf("Render() error = %v, want lib.libsonnet decode failure", err)
	}
}

// TestDirectoryRendererIncludeExcludeGlobV1Semantics pins directory
// include/exclude matching to github.com/gobwas/glob v1.0.0, which Argo CD
// v3.6.1 util/glob uses (821e090d6). v0.2.3 matched none of the empty
// brace-alternative patterns below and wrongly matched overlapping
// prefix/suffix patterns such as deploy*deploy.yaml against deploy.yaml.
func TestDirectoryRendererIncludeExcludeGlobV1Semantics(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"crds/a.yaml",
		"x/crds/b.yaml",
		"base/c.yaml",
		"app/base/d.yaml",
		"deploy.yaml",
		"x.deploy.yaml",
		"deploy-mid-deploy.yaml",
		"other.yaml",
	} {
		name := strings.NewReplacer("/", "-", ".yaml", "").Replace(rel)
		writeFile(t, filepath.Join(root, "apps", filepath.FromSlash(rel)), directoryConfigMap(name))
	}

	for _, tt := range []struct {
		name    string
		include string
		exclude string
		want    []string
	}{
		{name: "optional double-star prefix", include: "{**/,}crds/*.yaml", want: []string{"crds-a", "x-crds-b"}},
		{name: "optional single-star prefix", include: "{*/,}base/*.yaml", want: []string{"app-base-d", "base-c"}},
		{name: "optional dotted prefix", include: "{*.,}deploy.yaml", want: []string{"deploy", "x.deploy"}},
		{name: "overlapping prefix and suffix", include: "deploy*deploy.yaml", want: []string{"deploy-mid-deploy"}},
		{name: "exclude optional double-star prefix", exclude: "{**/,}crds/*.yaml", want: []string{"app-base-d", "base-c", "deploy", "deploy-mid-deploy", "other", "x.deploy"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{
				DirectoryRecurse: true,
				DirectoryInclude: tt.include,
				DirectoryExclude: tt.exclude,
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if got := directoryManifestNames(result); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rendered names = %#v, want %#v", got, tt.want)
			}
		})
	}
}
