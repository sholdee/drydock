package paritycompare

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// argocdApplicationList is trimmed `kubectl get applications -o json`
// output: one single-source Application and one multi-source Application,
// with the status and metadata noise a live List carries.
const argocdApplicationList = `{
  "apiVersion": "v1",
  "kind": "List",
  "items": [
    {
      "apiVersion": "argoproj.io/v1alpha1",
      "kind": "Application",
      "metadata": {
        "name": "parity-oci-dir-alpha",
        "namespace": "argocd",
        "ownerReferences": [{"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet", "name": "parity-oci-directories", "uid": "1"}]
      },
      "spec": {
        "project": "default",
        "source": {
          "repoURL": "oci://registry.example.invalid/parity/appset",
          "targetRevision": "v1.0.0",
          "path": "apps/alpha",
          "kustomize": {"namePrefix": "apps-"}
        },
        "destination": {"name": "in-cluster", "namespace": "parity-oci-dir-alpha"}
      },
      "status": {"sync": {"status": "OutOfSync"}}
    },
    {
      "apiVersion": "argoproj.io/v1alpha1",
      "kind": "Application",
      "metadata": {"name": "parity-multi", "namespace": "parity-tenant"},
      "spec": {
        "project": "tenant",
        "sources": [
          {"repoURL": "git://example.invalid/repo.git", "targetRevision": "HEAD", "path": "workloads/multi"},
          {"repoURL": "git://example.invalid/repo.git", "targetRevision": "HEAD", "ref": "values"},
          {"repoURL": "registry.example.invalid", "targetRevision": "1.0.0", "chart": "demo"}
        ],
        "destination": {"server": "https://kubernetes.default.svc", "namespace": "parity-multi"}
      }
    }
  ]
}`

// drydockApplicationList is the matching `drydock get apps -o json` output.
const drydockApplicationList = `[
  {
    "destination": {"name": "in-cluster", "namespace": "parity-oci-dir-alpha", "server": ""},
    "name": "parity-oci-dir-alpha",
    "namespace": "argocd",
    "project": "default",
    "sources": [
      {"path": "apps/alpha", "repoURL": "oci://registry.example.invalid/parity/appset", "targetRevision": "v1.0.0"}
    ]
  },
  {
    "destination": {"name": "", "namespace": "parity-multi", "server": "https://kubernetes.default.svc"},
    "name": "parity-multi",
    "namespace": "parity-tenant",
    "project": "tenant",
    "sources": [
      {"path": "workloads/multi", "repoURL": "git://example.invalid/repo.git", "targetRevision": "HEAD"},
      {"ref": "values", "repoURL": "git://example.invalid/repo.git", "targetRevision": "HEAD"},
      {"chart": "demo", "repoURL": "registry.example.invalid", "targetRevision": "1.0.0"}
    ]
  }
]`

func compareApplicationLists(t *testing.T, argocd, drydock string) (ApplicationResult, string, error) {
	t.Helper()
	root := t.TempDir()
	argocdFile := filepath.Join(root, "argocd.json")
	drydockFile := filepath.Join(root, "drydock.json")
	writeFile(t, argocdFile, argocd)
	writeFile(t, drydockFile, drydock)
	outDir := filepath.Join(root, "out")
	result, err := CompareApplications(ApplicationOptions{ArgoCDFile: argocdFile, DrydockFile: drydockFile, OutDir: outDir})
	return result, outDir, err
}

func TestCompareApplicationsMatchesProjectedSpecs(t *testing.T) {
	result, outDir, err := compareApplicationLists(t, argocdApplicationList, drydockApplicationList)
	if err != nil {
		t.Fatalf("CompareApplications() error = %v", err)
	}
	if result.Failed() || result.Applications != 2 || result.DiffFile != "" {
		t.Fatalf("CompareApplications() = %#v, want two matching Applications", result)
	}
	for _, side := range []string{"argocd", "drydock"} {
		data, err := os.ReadFile(filepath.Join(outDir, side+"-applications.json"))
		if err != nil {
			t.Fatalf("read %s inventory: %v", side, err)
		}
		if !strings.Contains(string(data), `"name": "parity-oci-dir-alpha"`) {
			t.Fatalf("%s inventory misses parity-oci-dir-alpha:\n%s", side, data)
		}
	}
}

func TestCompareApplicationsReportsNamesAndSpecs(t *testing.T) {
	// drydock lists a hidden directory Argo CD never generates, misses the
	// tenant Application, and renders a different path for alpha.
	drydock := `[
  {
    "destination": {"name": "in-cluster", "namespace": "parity-oci-dir-alpha", "server": ""},
    "name": "parity-oci-dir-alpha",
    "namespace": "argocd",
    "project": "default",
    "sources": [{"path": "apps/beta", "repoURL": "oci://registry.example.invalid/parity/appset", "targetRevision": "v1.0.0"}]
  },
  {
    "destination": {"name": "in-cluster", "namespace": "parity-oci-dir-hidden", "server": ""},
    "name": "parity-oci-dir-hidden",
    "namespace": "argocd",
    "project": "default",
    "sources": [{"path": "apps/.hidden", "repoURL": "oci://registry.example.invalid/parity/appset", "targetRevision": "v1.0.0"}]
  }
]`
	result, _, err := compareApplicationLists(t, argocdApplicationList, drydock)
	if err != nil {
		t.Fatalf("CompareApplications() error = %v", err)
	}
	if !result.Failed() || result.Applications != 3 {
		t.Fatalf("CompareApplications() = %#v, want a failed comparison of three Applications", result)
	}
	if !slices.Equal(result.Missing, []string{"parity-tenant/parity-multi"}) {
		t.Fatalf("Missing = %q", result.Missing)
	}
	if !slices.Equal(result.Extra, []string{"argocd/parity-oci-dir-hidden"}) {
		t.Fatalf("Extra = %q", result.Extra)
	}
	if !slices.Equal(result.SpecDifferences, []string{"argocd/parity-oci-dir-alpha"}) {
		t.Fatalf("SpecDifferences = %q", result.SpecDifferences)
	}
	diff, err := os.ReadFile(result.DiffFile)
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	for _, want := range []string{`-      "path": "apps/alpha",`, `+      "path": "apps/beta",`, "+++ drydock/argocd/parity-oci-dir-hidden", "--- argocd/parity-tenant/parity-multi"} {
		if !strings.Contains(string(diff), want) {
			t.Fatalf("diff misses %q:\n%s", want, diff)
		}
	}
}

func TestCompareApplicationsRejectsUnknownDrydockFields(t *testing.T) {
	drydock := strings.Replace(drydockApplicationList, `"project": "default",`, `"project": "default", "syncPolicy": {},`, 1)
	if _, _, err := compareApplicationLists(t, argocdApplicationList, drydock); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("CompareApplications() error = %v, want an unknown field error", err)
	}
}

func TestCompareApplicationsRejectsDuplicates(t *testing.T) {
	drydock := `[{"name": "a", "namespace": "argocd", "project": "default", "destination": {"name": "", "server": "", "namespace": ""}, "sources": []},
{"name": "a", "namespace": "argocd", "project": "default", "destination": {"name": "", "server": "", "namespace": ""}, "sources": []}]`
	if _, _, err := compareApplicationLists(t, argocdApplicationList, drydock); err == nil || !strings.Contains(err.Error(), "duplicate Application argocd/a") {
		t.Fatalf("CompareApplications() error = %v, want a duplicate error", err)
	}
}

func TestCompareApplicationsRejectsEmptyArgoCDInventory(t *testing.T) {
	if _, _, err := compareApplicationLists(t, `{"items": []}`, `[]`); err == nil || !strings.Contains(err.Error(), "holds no Applications") {
		t.Fatalf("CompareApplications() error = %v, want an empty Argo CD inventory error", err)
	}
}

func TestCompareApplicationsRejectsTrailingDrydockData(t *testing.T) {
	for _, trailing := range []string{"[]", "{}", "warning: extra output"} {
		if _, _, err := compareApplicationLists(t, argocdApplicationList, drydockApplicationList+"\n"+trailing); err == nil || !strings.Contains(err.Error(), "unexpected data after the Application list") {
			t.Fatalf("CompareApplications() with trailing %q error = %v, want a trailing data error", trailing, err)
		}
	}
}
