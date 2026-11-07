package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testNamespace = "parity-directory-decode"

const testExpected = `cases:
  - name: parity-decode-json-multiple
    file: multiple.json
    argocdMessage: Found multiple objects in "multiple.json"
    drydockMessage: "multiple.json: found multiple objects"
  - name: parity-decode-yaml-partial
    file: partial.yaml
    argocdMessage: Failed to unmarshal "partial.yaml"
    drydockMessage: "partial.yaml document 1: decode YAML document failed"
`

type testFixture struct {
	root     string
	expected string
	appDir   string
	drydock  string
}

func newTestFixture(t *testing.T, expected string) testFixture {
	t.Helper()
	root := t.TempDir()
	fixture := testFixture{
		root:     root,
		expected: filepath.Join(root, "expected.yaml"),
		appDir:   filepath.Join(root, "apps"),
		drydock:  filepath.Join(root, "drydock"),
	}
	for _, dir := range []string{fixture.appDir, fixture.drydock} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
	}
	writeFile(t, fixture.expected, expected)
	return fixture
}

func (fixture testFixture) options() options {
	return options{
		Expected:     fixture.expected,
		Namespace:    testNamespace,
		ArgoCDAppDir: fixture.appDir,
		DrydockDir:   fixture.drydock,
	}
}

func (fixture testFixture) writeApplication(t *testing.T, namespace, name string, conditions ...appCondition) {
	t.Helper()
	var app appStatusFile
	app.Metadata.Name = name
	app.Metadata.Namespace = namespace
	app.Status.Conditions = conditions
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeFile(t, filepath.Join(fixture.appDir, name+".json"), string(data))
}

func (fixture testFixture) writeDrydock(t *testing.T, name string, exitCode int, stderr string) {
	t.Helper()
	writeFile(t, filepath.Join(fixture.drydock, name+".exit"), strconv.Itoa(exitCode)+"\n")
	writeFile(t, filepath.Join(fixture.drydock, name+".stderr"), stderr)
}

func comparisonError(message string) appCondition {
	return appCondition{Type: comparisonErrorType, Message: message}
}

// writePassingCases writes captures that match testExpected on both sides,
// with the message text Argo CD v3.6.1 and drydock actually print.
func (fixture testFixture) writePassingCases(t *testing.T) {
	t.Helper()
	fixture.writeApplication(t, testNamespace, "parity-decode-json-multiple",
		comparisonError(`Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = FailedPrecondition desc = Found multiple objects in "multiple.json". Only single objects are allowed in JSON files.`))
	fixture.writeDrydock(t, "parity-decode-json-multiple", 2,
		"error render.failed: Application parity-directory-decode/parity-decode-json-multiple failed to render: workloads/directory-decode/json-multiple/multiple.json: found multiple objects; only single objects are allowed in JSON files\n")
	fixture.writeApplication(t, testNamespace, "parity-decode-yaml-partial",
		appCondition{Type: "SyncError", Message: "unrelated"},
		comparisonError(`Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = FailedPrecondition desc = Manifest generation error (cached): Failed to unmarshal "partial.yaml": failed to unmarshal manifest: error converting YAML to JSON`))
	fixture.writeDrydock(t, "parity-decode-yaml-partial", 2,
		"error render.failed: workloads/directory-decode/yaml-partial/partial.yaml document 1: decode YAML document failed: error converting YAML to JSON\n")
}

func TestComparePassesWhenBothSidesFailOnTheSameFile(t *testing.T) {
	fixture := newTestFixture(t, testExpected)
	fixture.writePassingCases(t)

	result, err := compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("compare() failures = %v, want none", result.Failures)
	}
	summary := result.Summary()
	for _, want := range []string{
		"Cases: 2",
		"Failures: 0",
		"ok parity-directory-decode/parity-decode-json-multiple",
		"ok parity-directory-decode/parity-decode-yaml-partial",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("Summary() = %q, want %q", summary, want)
		}
	}
}

// TestCompareMatchesQuotedObjectMessage covers a .json object without a kind.
// The Argo CD controller message quotes the manifest and names no file.
func TestCompareMatchesQuotedObjectMessage(t *testing.T) {
	fixture := newTestFixture(t, `cases:
  - name: parity-decode-json-object
    file: empty-object.json
    argocdQuotesObject: true
    argocdMessage: "Object 'Kind' is missing in '{\"metadata\":{\"annotations\":{\"argocd.argoproj.io/tracking-id\":"
    drydockMessage: "empty-object.json: JSON object has no kind"
`)
	fixture.writeApplication(t, testNamespace, "parity-decode-json-object",
		comparisonError(`Failed to load target state: failed to unmarshal manifests for source 1 of 1: Object 'Kind' is missing in '{"metadata":{"annotations":{"argocd.argoproj.io/tracking-id":"parity-directory-decode_parity-decode-json-object:/:parity-directory-decode-workloads/"}}}'`))
	fixture.writeDrydock(t, "parity-decode-json-object", 2,
		"error render.failed: workloads/directory-decode/json-object/empty-object.json: JSON object has no kind, so Argo CD fails the Application with \"Object 'Kind' is missing\"\n")

	result, err := compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("compare() failures = %v, want none", result.Failures)
	}

	// A missing kind on some other object must not pass the case.
	fixture.writeApplication(t, testNamespace, "parity-decode-json-object",
		comparisonError(`Failed to load target state: failed to unmarshal manifests for source 1 of 1: Object 'Kind' is missing in '{"apiVersion":"v1","metadata":{"annotations":{"argocd.argoproj.io/tracking-id":"x"}}}'`))
	result, err = compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "does not contain") {
		t.Fatalf("compare() failures = %v, want one message mismatch", result.Failures)
	}
}

// TestCompareMatchesPluginOutputMessage covers plugin output that Argo CD
// cannot decode. The Argo CD repo-server message quotes the object, and the
// drydock message names the plugin output instead of the plugin program.
func TestCompareMatchesPluginOutputMessage(t *testing.T) {
	fixture := newTestFixture(t, `cases:
  - name: parity-decode-plugin-empty-object
    source: plugin
    file: generate.awk
    argocdQuotesObject: true
    argocdMessage: "Object 'Kind' is missing in '{}'"
    drydockMessage: "generate-output document 1 has no kind"
`)
	fixture.writeApplication(t, testNamespace, "parity-decode-plugin-empty-object",
		comparisonError(`Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = Unknown desc = CMP processing failed for application "parity-directory-decode_parity-decode-plugin-empty-object": failed to convert CMP manifests to unstructured objects: failed to unmarshal manifest: error unmarshaling JSON: while decoding JSON: Object 'Kind' is missing in '{}'`))
	fixture.writeDrydock(t, "parity-decode-plugin-empty-object", 2,
		"error render.failed: config management plugin parity-env produced invalid generated manifests for source path \"workloads/directory-decode/plugin-empty-object\" at plugin/parity-env/path/workloads/directory-decode/plugin-empty-object/generate-output: plugin/parity-env/path/workloads/directory-decode/plugin-empty-object/generate-output document 1 has no kind, so Argo CD fails the Application with \"Object 'Kind' is missing\"\n")

	result, err := compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("compare() failures = %v, want none", result.Failures)
	}
}

// TestCompareMatchesNamedObjectMessage covers tracking metadata that cannot
// be set. The Argo CD repo-server message names the object and no file.
func TestCompareMatchesNamedObjectMessage(t *testing.T) {
	fixture := newTestFixture(t, `cases:
  - name: parity-decode-yaml-tracking
    file: tracking.yaml
    argocdNamesObject: parity-decode-tracking-annotations
    argocdMessage: "failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap parity-directory-decode-workloads/parity-decode-tracking-annotations"
    drydockMessage: "tracking.yaml document 0 list item /items/0: failed to set app instance tracking info on manifest"
`)
	fixture.writeApplication(t, testNamespace, "parity-decode-yaml-tracking",
		comparisonError(`Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = Unknown desc = failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap parity-directory-decode-workloads/parity-decode-tracking-annotations: .metadata.annotations accessor error: not-a-map is of the type string, expected map[string]interface{}`))
	fixture.writeDrydock(t, "parity-decode-yaml-tracking", 2,
		"error render.failed: workloads/directory-decode/yaml-tracking/tracking.yaml document 0 list item /items/0: failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap parity-directory-decode-workloads/parity-decode-tracking-annotations\n")

	result, err := compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("compare() failures = %v, want none", result.Failures)
	}

	// The same failure on some other object must not pass the case.
	fixture.writeApplication(t, testNamespace, "parity-decode-yaml-tracking",
		comparisonError(`Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = Unknown desc = failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap parity-directory-decode-workloads/other: .metadata.annotations accessor error`))
	result, err = compare(fixture.options())
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "does not contain") {
		t.Fatalf("compare() failures = %v, want one message mismatch", result.Failures)
	}
}

func TestCompareReportsMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture testFixture)
		want   string
	}{
		{
			name: "Argo CD has no ComparisonError",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, testNamespace, "parity-decode-json-multiple")
			},
			want: "Argo CD has no ComparisonError condition",
		},
		{
			name: "Argo CD fails on another file",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, testNamespace, "parity-decode-json-multiple",
					comparisonError(`Failed to unmarshal "configmap.yaml"`))
			},
			want: `does not contain "Found multiple objects in \"multiple.json\""`,
		},
		{
			name: "Argo CD message on another condition type",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, testNamespace, "parity-decode-json-multiple",
					appCondition{Type: "InvalidSpecError", Message: `Found multiple objects in "multiple.json"`})
			},
			want: "Argo CD has no ComparisonError condition",
		},
		{
			name: "Argo CD capture missing",
			mutate: func(t *testing.T, fixture testFixture) {
				if err := os.Remove(filepath.Join(fixture.appDir, "parity-decode-json-multiple.json")); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			want: "missing Argo CD Application JSON capture",
		},
		{
			name: "Argo CD capture from another namespace",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "argocd", "parity-decode-json-multiple",
					comparisonError(`Found multiple objects in "multiple.json"`))
			},
			want: "Argo CD capture is Application argocd/parity-decode-json-multiple",
		},
		{
			name: "drydock rendered",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t, "parity-decode-json-multiple", 0, "multiple.json: found multiple objects\n")
			},
			want: "drydock rendered the Application, want a render failure",
		},
		{
			name: "drydock fails on another file",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t, "parity-decode-json-multiple", 2, "configmap.yaml document 0 is missing kind\n")
			},
			want: `drydock stderr does not contain "multiple.json: found multiple objects"`,
		},
		{
			name: "drydock exit code missing",
			mutate: func(t *testing.T, fixture testFixture) {
				if err := os.Remove(filepath.Join(fixture.drydock, "parity-decode-json-multiple.exit")); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			want: "missing drydock exit code capture",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTestFixture(t, testExpected)
			fixture.writePassingCases(t)
			tt.mutate(t, fixture)

			result, err := compare(fixture.options())
			if err != nil {
				t.Fatalf("compare() error = %v", err)
			}
			if len(result.Failures) == 0 {
				t.Fatalf("compare() failures = none, want %q", tt.want)
			}
			joined := strings.Join(result.Failures, "\n")
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("compare() failures = %q, want %q", joined, tt.want)
			}
			if !strings.Contains(joined, "parity-directory-decode/parity-decode-json-multiple: ") {
				t.Fatalf("compare() failures = %q, want them keyed by the failing case", joined)
			}
			if !strings.Contains(result.Summary(), "ok parity-directory-decode/parity-decode-yaml-partial") {
				t.Fatalf("Summary() = %q, want the untouched case to pass", result.Summary())
			}
		})
	}
}

func TestReadExpectedRejectsInvalidCases(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		want     string
	}{
		{name: "no cases", expected: "cases: []\n", want: "contains no cases"},
		{
			name:     "duplicate",
			expected: testExpected + strings.SplitN(testExpected, "\n", 2)[1],
			want:     "duplicate expected case",
		},
		{
			name: "missing file",
			expected: `cases:
  - name: parity-decode-json-empty
    argocdMessage: Failed to unmarshal
    drydockMessage: decode JSON object failed
`,
			want: "must name the failing file",
		},
		{
			name: "Argo CD message without file",
			expected: `cases:
  - name: parity-decode-json-empty
    file: empty.json
    argocdMessage: Failed to unmarshal
    drydockMessage: "empty.json: decode JSON object failed"
`,
			want: `argocdMessage must contain the file name "empty.json"`,
		},
		{
			name: "quoted object without missing kind prefix",
			expected: `cases:
  - name: parity-decode-json-object
    file: empty-object.json
    argocdQuotesObject: true
    argocdMessage: Failed to unmarshal
    drydockMessage: "empty-object.json: JSON object has no kind"
`,
			want: "argocdMessage must start with",
		},
		{
			name: "quoted object with only the prefix",
			expected: `cases:
  - name: parity-decode-json-object
    file: empty-object.json
    argocdQuotesObject: true
    argocdMessage: "Object 'Kind' is missing in '{"
    drydockMessage: "empty-object.json: JSON object has no kind"
`,
			want: "quote part of the object",
		},
		{
			name: "named object missing from the Argo CD message",
			expected: `cases:
  - name: parity-decode-yaml-tracking
    file: tracking.yaml
    argocdNamesObject: parity-decode-tracking-annotations
    argocdMessage: failed to set app instance tracking info on manifest
    drydockMessage: "tracking.yaml document 0 list item /items/0: failed to set app instance tracking info on manifest"
`,
			want: `argocdMessage must contain the object name "parity-decode-tracking-annotations"`,
		},
		{
			name: "quoted and named object",
			expected: `cases:
  - name: parity-decode-yaml-tracking
    file: tracking.yaml
    argocdQuotesObject: true
    argocdNamesObject: parity-decode-tracking-annotations
    argocdMessage: "Object 'Kind' is missing in '{parity-decode-tracking-annotations"
    drydockMessage: "tracking.yaml document 0 list item /items/0: failed to set app instance tracking info on manifest"
`,
			want: "must not set both argocdQuotesObject and argocdNamesObject",
		},
		{
			name: "drydock message without file",
			expected: `cases:
  - name: parity-decode-json-empty
    file: empty.json
    argocdMessage: Failed to unmarshal "empty.json"
    drydockMessage: decode JSON object failed
`,
			want: `drydockMessage must contain the file name "empty.json"`,
		},
		{
			name: "unknown source",
			expected: `cases:
  - name: parity-decode-json-empty
    source: jsonnet
    file: empty.json
    argocdMessage: Failed to unmarshal "empty.json"
    drydockMessage: "empty.json: decode JSON object failed"
`,
			want: `has source "jsonnet"`,
		},
		{
			name: "plugin message without the output name",
			expected: `cases:
  - name: parity-decode-plugin-empty-object
    source: plugin
    file: generate.awk
    argocdQuotesObject: true
    argocdMessage: "Object 'Kind' is missing in '{}'"
    drydockMessage: "generate.awk document 1 has no kind"
`,
			want: `drydockMessage must contain the plugin output name "generate-output"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTestFixture(t, tt.expected)
			_, err := readExpected(fixture.expected)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readExpected() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCompareRequiresFlags(t *testing.T) {
	_, err := compare(options{Expected: "expected.yaml"})
	if err == nil || !strings.Contains(err.Error(), "--namespace, --argocd-app-dir, --drydock-dir") {
		t.Fatalf("compare() error = %v, want missing flag list", err)
	}
}

// TestCommittedExpectedFileMatchesFixtures keeps the committed expected file
// valid and in step with the fixture repository: every case has an
// Application in the decode namespace and the failing file in its source
// directory.
func TestCommittedExpectedFileMatchesFixtures(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cases, err := readExpected(filepath.Join(repoRoot, "testdata", "argocd-parity", "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatalf("readExpected() error = %v", err)
	}
	fixtureRepo := filepath.Join(repoRoot, "testdata", "argocd-parity", "repo")
	appDir := filepath.Join(fixtureRepo, "directory-decode-applications")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != len(cases) {
		t.Fatalf("%s holds %d files, want one per expected case (%d)", appDir, len(entries), len(cases))
	}
	for _, tc := range cases {
		caseDir := strings.TrimPrefix(tc.Name, "parity-decode-")
		app, err := os.ReadFile(filepath.Join(appDir, caseDir+".yaml"))
		if err != nil {
			t.Fatalf("read Application for %s: %v", tc.Name, err)
		}
		if tc.Repository != "" {
			checkSeparateRepositoryCase(t, repoRoot, tc, string(app))
			continue
		}
		for _, want := range []string{
			"name: " + tc.Name + "\n",
			"namespace: " + testNamespace + "\n",
			"path: workloads/directory-decode/" + caseDir + "\n",
		} {
			if !strings.Contains(string(app), want) {
				t.Fatalf("Application for %s does not contain %q", tc.Name, want)
			}
		}
		if tc.SourceType() == sourceHelmFileParameters {
			// The file is a file parameter path or the file it resolves
			// to, which can be missing on purpose.
			if !strings.Contains(string(app), "fileParameters:\n") {
				t.Fatalf("Application for %s sets no helm.fileParameters", tc.Name)
			}
			continue
		}
		failing, err := os.ReadFile(filepath.Join(fixtureRepo, "workloads", "directory-decode", caseDir, tc.File))
		if err != nil {
			t.Fatalf("failing file for %s: %v", tc.Name, err)
		}
		if tc.ArgoCDNamesObject != "" && !strings.Contains(string(failing), "name: "+tc.ArgoCDNamesObject+"\n") {
			t.Fatalf("failing file for %s does not hold the object %q", tc.Name, tc.ArgoCDNamesObject)
		}
	}
}

// checkSeparateRepositoryCase checks a case whose Application renders from
// a fixture repository other than the main one. The smoke builds that
// repository and adds the failing file.
func checkSeparateRepositoryCase(t *testing.T, repoRoot string, tc expectedCase, app string) {
	t.Helper()
	for _, want := range []string{
		"name: " + tc.Name + "\n",
		"namespace: " + testNamespace + "\n",
		"repoURL: git://argocd-parity-git.argocd-parity.svc.cluster.local/" + tc.Repository + ".git\n",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("Application for %s does not contain %q", tc.Name, want)
		}
	}
	if info, err := os.Stat(filepath.Join(repoRoot, "testdata", "argocd-parity", tc.Repository+"-repo")); err != nil || !info.IsDir() {
		t.Fatalf("fixture repository for %s: %v", tc.Name, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
