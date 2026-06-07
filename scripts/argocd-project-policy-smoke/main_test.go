package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareProjectPolicySmokePassesExpectedCases(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "apps")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeApplicationJSON(t, appDir, "argocd", "project-policy-source-allowed")
	writeApplicationJSON(t, appDir, "argocd", "project-policy-source-denied", invalidSpecCondition("source repo is not permitted"))
	writeApplicationJSON(t, appDir, "argocd", "project-policy-destination-allowed")
	writeApplicationJSON(t, appDir, "argocd", "project-policy-destination-denied", invalidSpecCondition("destination is not permitted"))
	writeApplicationJSON(t, appDir, "project-policy-tenant", "project-policy-source-namespace-allowed")
	writeApplicationJSON(t, appDir, "project-policy-tenant", "project-policy-source-namespace-denied", unknownErrorCondition("application 'project-policy-source-namespace-denied' in namespace 'project-policy-tenant' is not permitted to use project 'project-policy-source-namespace-denied'"))

	expectedPath := filepath.Join(root, "expected.yaml")
	writeFile(t, expectedPath, `cases:
  - name: project-policy-source-allowed
    namespace: argocd
    argocdCondition: none
    drydockDiagnosticCode: ""
  - name: project-policy-source-denied
    namespace: argocd
    argocdCondition: source
    drydockDiagnosticCode: project.source-repository-denied
  - name: project-policy-destination-allowed
    namespace: argocd
    argocdCondition: none
    drydockDiagnosticCode: ""
  - name: project-policy-destination-denied
    namespace: argocd
    argocdCondition: destination
    drydockDiagnosticCode: project.destination-denied
  - name: project-policy-source-namespace-allowed
    namespace: project-policy-tenant
    argocdCondition: none
    drydockDiagnosticCode: ""
  - name: project-policy-source-namespace-denied
    namespace: project-policy-tenant
    argocdCondition: source-namespace
    drydockDiagnosticCode: project.source-namespace-denied
`)
	diagnosticsPath := filepath.Join(root, "drydock.json")
	writeFile(t, diagnosticsPath, `{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    },
    {
      "code": "project.destination-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-destination-denied destination is not permitted",
      "provenance": {"path": "argocd/project-policy-destination-denied"}
    },
    {
      "code": "project.source-namespace-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application project-policy-tenant/project-policy-source-namespace-denied source namespace is not permitted",
      "provenance": {"path": "project-policy-tenant/project-policy-source-namespace-denied"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("Failures = %#v, want none\n%s", result.Failures, result.Summary())
	}
	if !strings.Contains(result.Summary(), "Cases: 6\nFailures: 0\n") {
		t.Fatalf("Summary() = %q, want case and failure counts", result.Summary())
	}
}

func TestCompareReportsArgoCDCategoryMismatch(t *testing.T) {
	root, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(t, invalidSpecCondition("destination is not permitted"), `{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "Argo CD policy condition categories got destination, want source") {
		t.Fatalf("Failures = %#v, want source category mismatch in %s", result.Failures, root)
	}
}

func TestCompareReportsExtraArgoCDCategoryForDeniedCase(t *testing.T) {
	root, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(
		t,
		invalidSpecCondition("source repo is not permitted"),
		`{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    }
  ]
}`,
	)
	writeApplicationJSON(t, appDir, "argocd", "project-policy-source-denied",
		invalidSpecCondition("source repo is not permitted"),
		invalidSpecCondition("destination is not permitted"),
	)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "Argo CD policy condition categories got destination, source, want source") {
		t.Fatalf("Failures = %#v, want extra Argo CD category failure in %s", result.Failures, root)
	}
}

func TestCompareReportsMissingDrydockDiagnosticCode(t *testing.T) {
	_, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(t, invalidSpecCondition("source repo is not permitted"), `{"diagnostics": []}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "drydock diagnostic codes got none, want project.source-repository-denied") {
		t.Fatalf("Failures = %#v, want missing drydock code", result.Failures)
	}
}

func TestCompareReportsExtraDrydockDiagnosticForDeniedCase(t *testing.T) {
	_, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(t, invalidSpecCondition("source repo is not permitted"), `{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    },
    {
      "code": "project.destination-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied destination is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "drydock diagnostic codes got project.destination-denied, project.source-repository-denied, want project.source-repository-denied") {
		t.Fatalf("Failures = %#v, want extra drydock code", result.Failures)
	}
}

func TestCompareMatchesDrydockDiagnosticsByExactProvenance(t *testing.T) {
	_, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(t, invalidSpecCondition("source repo is not permitted"), `{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "other/project-policy-source-denied"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "drydock diagnostic codes got none, want project.source-repository-denied") {
		t.Fatalf("Failures = %#v, want exact provenance failure", result.Failures)
	}
}

func TestCompareRequiresExplicitDrydockDiagnosticCode(t *testing.T) {
	_, appDir, expectedPath, diagnosticsPath := writeSingleCaseInputs(t, invalidSpecCondition("source repo is not permitted"), `{
  "diagnostics": [
    {
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-denied source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-denied"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "drydock diagnostic codes got none, want project.source-repository-denied") {
		t.Fatalf("Failures = %#v, want explicit code failure", result.Failures)
	}
}

func TestCompareReportsUnexpectedDrydockDiagnosticForAllowedCase(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "apps")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeApplicationJSON(t, appDir, "argocd", "project-policy-source-allowed")
	expectedPath := filepath.Join(root, "expected.yaml")
	writeFile(t, expectedPath, `cases:
  - name: project-policy-source-allowed
    namespace: argocd
    argocdCondition: none
    drydockDiagnosticCode: ""
`)
	diagnosticsPath := filepath.Join(root, "drydock.json")
	writeFile(t, diagnosticsPath, `{
  "diagnostics": [
    {
      "code": "project.source-repository-denied",
      "severity": "warning",
      "category": "project",
      "message": "Application argocd/project-policy-source-allowed source repository is not permitted",
      "provenance": {"path": "argocd/project-policy-source-allowed"}
    }
  ]
}`)

	result, err := compare(options{
		ArgoCDAppDir:       appDir,
		DrydockDiagnostics: diagnosticsPath,
		Expected:           expectedPath,
	})
	if err != nil {
		t.Fatalf("compare() error = %v", err)
	}
	if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "drydock diagnostic codes got project.source-repository-denied, want none") {
		t.Fatalf("Failures = %#v, want unexpected drydock code", result.Failures)
	}
}

func writeSingleCaseInputs(t *testing.T, condition appCondition, diagnosticsJSON string) (string, string, string, string) {
	t.Helper()

	root := t.TempDir()
	appDir := filepath.Join(root, "apps")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeApplicationJSON(t, appDir, "argocd", "project-policy-source-denied", condition)
	expectedPath := filepath.Join(root, "expected.yaml")
	writeFile(t, expectedPath, `cases:
  - name: project-policy-source-denied
    namespace: argocd
    argocdCondition: source
    drydockDiagnosticCode: project.source-repository-denied
`)
	diagnosticsPath := filepath.Join(root, "drydock.json")
	writeFile(t, diagnosticsPath, diagnosticsJSON)
	return root, appDir, expectedPath, diagnosticsPath
}

func writeApplicationJSON(t *testing.T, dir, namespace, name string, conditions ...appCondition) {
	t.Helper()

	payload := appStatusFile{}
	payload.Metadata.Namespace = namespace
	payload.Metadata.Name = name
	payload.Status.Conditions = conditions
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	writeFile(t, filepath.Join(dir, namespace+"_"+name+".json"), string(data))
}

func invalidSpecCondition(message string) appCondition {
	return appCondition{
		Type:    "InvalidSpecError",
		Message: message,
	}
}

func unknownErrorCondition(message string) appCondition {
	return appCondition{
		Type:    "UnknownError",
		Message: message,
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
