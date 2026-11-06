package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testExpected = `targetRevision: live-diff-b
applications:
  - name: parity-live-diff-application
    resources:
      - kind: ConfigMap
        name: pointer-ignored
        status: Synced
      - group: apps
        kind: Deployment
        name: jq-other
        status: OutOfSync
    drydockWarnings:
      - "diff.ignore-pointer-invalid: Application argocd/parity-live-diff-application:"
  - name: parity-live-diff-annotation
    resources:
      - kind: ConfigMap
        name: pointer-ignored
        status: Synced
  - name: parity-live-diff-invalid-jq
    comparisonError:
      argocdMessage: failed to create diff normalizer
      drydockMessage: "diff.ignore-rule-invalid: Application argocd/parity-live-diff-invalid-jq"
    resources:
      - kind: ConfigMap
        name: invalid-jq
        status: Unknown
`

type testFixture struct {
	expected   string
	appDir     string
	drydock    string
	stderr     string
	failureDir string
}

func newTestFixture(t *testing.T, expected string) testFixture {
	t.Helper()
	root := t.TempDir()
	fixture := testFixture{
		expected: filepath.Join(root, "expected.yaml"),
		appDir:   filepath.Join(root, "apps"),
		drydock:  filepath.Join(root, "drydock-diff.json"),
		stderr:   filepath.Join(root, "drydock-diff.stderr"),

		failureDir: filepath.Join(root, "failures"),
	}
	for _, dir := range []string{fixture.appDir, fixture.failureDir} {
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
		ArgoCDAppDir: fixture.appDir,
		DrydockDiff:  fixture.drydock,

		DrydockStderr:     fixture.stderr,
		DrydockFailureDir: fixture.failureDir,
	}
}

func (fixture testFixture) writeApplication(t *testing.T, name, revision, syncStatus string, resources ...appResourceStatus) {
	t.Helper()
	fixture.writeApplicationWithConditions(t, name, revision, syncStatus, nil, resources...)
}

func (fixture testFixture) writeApplicationWithConditions(t *testing.T, name, revision, syncStatus string, conditions []appCondition, resources ...appResourceStatus) {
	t.Helper()
	var app appStatusFile
	app.Status.Conditions = conditions
	app.Metadata.Name = name
	app.Metadata.Namespace = argocdNamespace
	app.Status.Sync.Status = syncStatus
	app.Status.Sync.ComparedTo.Source.TargetRevision = revision
	app.Status.Resources = resources
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeFile(t, filepath.Join(fixture.appDir, name+".json"), string(data))
}

func (fixture testFixture) writeDrydock(t *testing.T, results ...drydockResult) {
	t.Helper()
	if results == nil {
		results = []drydockResult{}
	}
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeFile(t, fixture.drydock, string(data))
}

func resourceStatus(group, kind, name, status string) appResourceStatus {
	return appResourceStatus{Group: group, Kind: kind, Name: name, Status: status}
}

func drydockChange(app, group, kind, name, change string) drydockResult {
	var result drydockResult
	result.Parent.Namespace = argocdNamespace
	result.Parent.Name = app
	result.Resource.Group = group
	result.Resource.Kind = kind
	result.Resource.Name = name
	result.Change = change
	return result
}

// writePassingCases writes captures that match testExpected on both sides.
func (fixture testFixture) writePassingCases(t *testing.T) {
	t.Helper()
	fixture.writeApplication(t, "parity-live-diff-application", "live-diff-b", statusOutOfSync,
		resourceStatus("", "ConfigMap", "pointer-ignored", statusSynced),
		resourceStatus("apps", "Deployment", "jq-other", statusOutOfSync))
	fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-b", statusSynced,
		resourceStatus("", "ConfigMap", "pointer-ignored", statusSynced))
	fixture.writeDrydock(t, drydockChange("parity-live-diff-application", "apps", "Deployment", "jq-other", "modified"))
	writeFile(t, fixture.stderr, "warning diff.ignore-pointer-invalid: Application argocd/parity-live-diff-application: spec.ignoreDifferences jsonPointer \"data/kept\" is not a valid JSON pointer\n")
	fixture.writeApplicationWithConditions(t, "parity-live-diff-invalid-jq", "live-diff-b", statusUnknown,
		[]appCondition{{Type: comparisonErrorType, Message: "Failed to compare desired state to live state: failed to create diff normalizer: unexpected token"}},
		resourceStatus("", "ConfigMap", "invalid-jq", statusUnknown))
	fixture.writeFailure(t, "parity-live-diff-invalid-jq", "2", "error diff.ignore-rule-invalid: Application argocd/parity-live-diff-invalid-jq: failed to compare desired states\n")
}

func (fixture testFixture) writeFailure(t *testing.T, name, exitCode, stderr string) {
	t.Helper()
	writeFile(t, filepath.Join(fixture.failureDir, name+".exit"), exitCode+"\n")
	writeFile(t, filepath.Join(fixture.failureDir, name+".stderr"), stderr)
}

func TestComparePassesWhenBothSidesMatchTheExpectedStatus(t *testing.T) {
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
		"Applications: 3",
		"Resources: 4",
		"Differences: 1",
		"Failures: 0",
		"ok argocd/parity-live-diff-application",
		"ok argocd/parity-live-diff-annotation",
		"ok argocd/parity-live-diff-invalid-jq",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("Summary() = %q, want %q", summary, want)
		}
	}
}

func TestCompareFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture testFixture)
		want   string
	}{
		{
			name: "drydock reports an ignored change",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t,
					drydockChange("parity-live-diff-application", "apps", "Deployment", "jq-other", "modified"),
					drydockChange("parity-live-diff-application", "", "ConfigMap", "pointer-ignored", "modified"))
			},
			want: "drydock reports ConfigMap/pointer-ignored modified, want no change",
		},
		{
			name: "drydock misses a change",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t)
			},
			want: "drydock reports no change for apps/Deployment/jq-other, want a change",
		},
		{
			name: "drydock reports an added resource",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t, drydockChange("parity-live-diff-application", "apps", "Deployment", "jq-other", "added"))
			},
			want: "drydock reports apps/Deployment/jq-other added, want modified",
		},
		{
			name: "drydock reports an unlisted resource",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t,
					drydockChange("parity-live-diff-application", "apps", "Deployment", "jq-other", "modified"),
					drydockChange("parity-live-diff-application", "", "ConfigMap", "extra", "added"))
			},
			want: "drydock reports ConfigMap/extra added, which the expected file does not list",
		},
		{
			name: "drydock reports an unlisted Application",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeDrydock(t,
					drydockChange("parity-live-diff-application", "apps", "Deployment", "jq-other", "modified"),
					drydockChange("parity-other", "", "ConfigMap", "extra", "modified"))
			},
			want: "drydock reports changes for Application parity-other",
		},
		{
			name: "Argo CD reports another resource status",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-b", statusSynced,
					resourceStatus("", "ConfigMap", "pointer-ignored", statusOutOfSync))
			},
			want: `Argo CD reports ConfigMap/pointer-ignored as "OutOfSync", want "Synced"`,
		},
		{
			name: "Argo CD reports another Application status",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-b", statusOutOfSync,
					resourceStatus("", "ConfigMap", "pointer-ignored", statusSynced))
			},
			want: `Argo CD Application sync status is "OutOfSync", want "Synced"`,
		},
		{
			name: "Argo CD compared another revision",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-a", statusSynced,
					resourceStatus("", "ConfigMap", "pointer-ignored", statusSynced))
			},
			want: `Argo CD compared targetRevision "live-diff-a", want "live-diff-b"`,
		},
		{
			name: "Argo CD has an unlisted resource",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-b", statusSynced,
					resourceStatus("", "ConfigMap", "pointer-ignored", statusSynced),
					resourceStatus("", "ConfigMap", "extra", statusSynced))
			},
			want: "Argo CD has resource ConfigMap/extra, which the expected file does not list",
		},
		{
			name: "Argo CD misses a resource",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-annotation", "live-diff-b", statusSynced)
			},
			want: "Argo CD has no resource ConfigMap/pointer-ignored",
		},
		{
			name: "drydock misses a warning",
			mutate: func(t *testing.T, fixture testFixture) {
				writeFile(t, fixture.stderr, "")
			},
			want: `drydock diff stderr does not contain "diff.ignore-pointer-invalid: Application argocd/parity-live-diff-application:"`,
		},
		{
			name: "drydock diffs a comparisonError Application",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeFailure(t, "parity-live-diff-invalid-jq", "0", "")
			},
			want: "drydock diffed the Application, want a failure",
		},
		{
			name: "drydock fails a comparisonError Application with another message",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeFailure(t, "parity-live-diff-invalid-jq", "2", "error render.failed\n")
			},
			want: `drydock stderr does not contain "diff.ignore-rule-invalid: Application argocd/parity-live-diff-invalid-jq"`,
		},
		{
			name: "Argo CD compares a comparisonError Application",
			mutate: func(t *testing.T, fixture testFixture) {
				fixture.writeApplication(t, "parity-live-diff-invalid-jq", "live-diff-b", statusSynced,
					resourceStatus("", "ConfigMap", "invalid-jq", statusSynced))
			},
			want: "Argo CD has no ComparisonError condition",
		},
		{
			name: "Argo CD capture is missing",
			mutate: func(t *testing.T, fixture testFixture) {
				if err := os.Remove(filepath.Join(fixture.appDir, "parity-live-diff-annotation.json")); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			want: "missing Argo CD Application JSON capture",
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
			if !strings.Contains(strings.Join(result.Failures, "\n"), tt.want) {
				t.Fatalf("compare() failures = %v, want one containing %q", result.Failures, tt.want)
			}
		})
	}
}

func TestReadExpectedRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		want     string
	}{
		{name: "no revision", expected: "applications: []\n", want: "must set targetRevision"},
		{name: "no applications", expected: "targetRevision: b\n", want: "lists no Applications"},
		{
			name:     "no resources",
			expected: "targetRevision: b\napplications:\n  - name: a\n",
			want:     "lists no resources",
		},
		{
			name:     "duplicate application",
			expected: "targetRevision: b\napplications:\n  - name: a\n    resources: [{kind: ConfigMap, name: c, status: Synced}]\n  - name: a\n    resources: [{kind: ConfigMap, name: c, status: Synced}]\n",
			want:     "duplicate expected Application: a",
		},
		{
			name:     "duplicate resource",
			expected: "targetRevision: b\napplications:\n  - name: a\n    resources: [{kind: ConfigMap, name: c, status: Synced}, {kind: ConfigMap, name: c, status: OutOfSync}]\n",
			want:     "lists ConfigMap/c twice",
		},
		{
			name:     "unknown status",
			expected: "targetRevision: b\napplications:\n  - name: a\n    resources: [{kind: ConfigMap, name: c, status: Unknown}]\n",
			want:     `has status "Unknown"`,
		},
		{
			name:     "comparisonError without Unknown status",
			expected: "targetRevision: b\napplications:\n  - name: a\n    comparisonError: {argocdMessage: x, drydockMessage: y}\n    resources: [{kind: ConfigMap, name: c, status: Synced}]\n",
			want:     "want Unknown for a comparisonError Application",
		},
		{
			name:     "comparisonError without messages",
			expected: "targetRevision: b\napplications:\n  - name: a\n    comparisonError: {argocdMessage: x}\n    resources: [{kind: ConfigMap, name: c, status: Unknown}]\n",
			want:     "must set argocdMessage and drydockMessage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "expected.yaml")
			writeFile(t, path, tt.expected)
			_, err := readExpected(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readExpected() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// The committed expected file must stay valid, and its Applications must
// match the fixture's Application manifests.
func TestCommittedExpectedFileMatchesFixtureApplications(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "argocd-parity", "live-diff")
	expected, err := readExpected(filepath.Join(root, "expected.yaml"))
	if err != nil {
		t.Fatalf("readExpected() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "repo", "applications"))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	names := make([]string, 0, len(expected.Applications))
	for _, app := range expected.Applications {
		names = append(names, app.Name)
	}
	if strings.Join(sortedStrings(files), ",") != strings.Join(sortedStrings(names), ",") {
		t.Fatalf("expected Applications %v, fixture Application files %v", names, files)
	}
}

func sortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return sortedNames(set)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
