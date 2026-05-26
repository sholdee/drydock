package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

func TestOrchestratorDiffAppsReportsManifestChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %s, want modified", result.Results[0].Change)
	}
}

func TestOrchestratorDiffAppsHonorsApplicationJSONPointerIgnores(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithReplicas(t, left, 1, "")
	writeDeploymentAppWithReplicas(t, right, 2, `  ignoreDifferences:
    - group: app*
      kind: Deploy*
      name: demo
      jsonPointers:
        - /spec/replicas
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want replicas-only diff ignored: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppsHonorsApplicationJQPathExpressions(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithSidecarImage(t, left, "example/sidecar:v1", "")
	writeDeploymentAppWithSidecarImage(t, right, "example/sidecar:v2", `  ignoreDifferences:
    - group: apps
      kind: Deployment
      name: demo
      jqPathExpressions:
        - .spec.template.spec.containers[] | select(.name == "sidecar")
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want sidecar image diff ignored: %#v", len(result.Results), result.Results)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want no diagnostics: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsHonorsGlobalResourceCustomizationJSONPointers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithReplicas(t, left, 1, "")
	writeDeploymentAppWithReplicas(t, right, 2, "")
	writeGlobalCustomization(t, right, `resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jsonPointers:
          - /spec/replicas
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want replicas-only diff ignored: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppsHonorsGlobalResourceCustomizationJQPathExpressions(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeWebhookApp(t, left, "left-ca")
	writeWebhookApp(t, right, "right-ca")
	writeGlobalCustomization(t, right, `resource.customizations: |
    admissionregistration.k8s.io/MutatingWebhookConfiguration:
      ignoreDifferences: |
        jqPathExpressions:
          - .webhooks[].clientConfig.caBundle
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want caBundle-only diff ignored: %#v", len(result.Results), result.Results)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want no diagnostics: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsInvalidJQPathExpressionReturnsError(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%t", strict), func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeDeploymentAppWithReplicas(t, left, 1, "")
			writeDeploymentAppWithReplicas(t, right, 2, `  ignoreDifferences:
    - group: apps
      kind: Deployment
      jqPathExpressions:
        - .spec.replicas)
`)

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:  left,
				RightPath: right,
				Strict:    strict,
				Unified:   3,
			})
			if err == nil {
				t.Fatal("DiffApps() error = nil, want invalid jq error")
			}
			if !strings.Contains(err.Error(), "normalize") || !strings.Contains(err.Error(), "jq") {
				t.Fatalf("DiffApps() error = %v, want jq normalization error", err)
			}
			if len(result.Results) != 0 {
				t.Fatalf("Results = %#v, want none on normalization error", result.Results)
			}
		})
	}
}

func TestOrchestratorDiffAppsHonorsApplicationManagedFieldsManagers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithManagedReplicas(t, left, 1, "", "")
	writeDeploymentAppWithManagedReplicas(t, right, 2, "kube-controller-manager", `  ignoreDifferences:
    - group: apps
      kind: Deployment
      name: demo
      managedFieldsManagers:
        - kube-controller-manager
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want managed replicas ignored: %#v", len(result.Results), result.Results)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want no diagnostics: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsHonorsGlobalManagedFieldsManagers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithManagedReplicas(t, left, 1, "", "")
	writeDeploymentAppWithManagedReplicas(t, right, 2, "kube-controller-manager", "")
	writeGlobalCustomization(t, right, `resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        managedFieldsManagers:
          - kube-controller-manager
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want managed replicas ignored: %#v", len(result.Results), result.Results)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want no diagnostics: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsDefaultCompareOptionsSuppressStatusOnlyDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeConfigMapStatusApp(t, left, "old")
	writeConfigMapStatusApp(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want status-only diff ignored by default: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppsCompareOptionsNoneKeepsStatusDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeConfigMapStatusApp(t, left, "old")
	writeConfigMapStatusApp(t, right, "new")
	writeCompareOptions(t, left, "none", false)
	writeCompareOptions(t, right, "none", false)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want status diff", len(result.Results))
	}
	diff := result.Results[0].Diff
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
}

func TestOrchestratorDiffAppsCompareOptionsIgnoreAggregatedRoles(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeAggregatedClusterRoleApp(t, left, "pods")
	writeAggregatedClusterRoleApp(t, right, "services")
	writeCompareOptions(t, left, "none", true)
	writeCompareOptions(t, right, "none", true)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want aggregated rules diff ignored: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppsDiffSettingsFixture(t *testing.T) {
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  filepath.Join("testdata", "diff-settings", "left"),
		RightPath: filepath.Join("testdata", "diff-settings", "right"),
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want none: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want exactly status plus data diffs: %#v", len(result.Results), result.Results)
	}

	byKey := map[string]string{}
	for _, item := range result.Results {
		key := item.Parent.Name + "/" + item.Resource.Kind + "/" + item.Resource.Name
		byKey[key] = item.Diff
	}
	statusDiff, ok := byKey["status/ConfigMap/status"]
	if !ok {
		t.Fatalf("Results = %#v, want status ConfigMap diff", result.Results)
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(statusDiff, want) {
			t.Fatalf("status diff missing %q:\n%s", want, statusDiff)
		}
	}
	workloadDiff, ok := byKey["workload/ConfigMap/workload-config"]
	if !ok {
		t.Fatalf("Results = %#v, want workload ConfigMap diff", result.Results)
	}
	for _, want := range []string{"-  mode: old", "+  mode: new"} {
		if !strings.Contains(workloadDiff, want) {
			t.Fatalf("workload diff missing %q:\n%s", want, workloadDiff)
		}
	}

	combinedDiff := statusDiff + workloadDiff
	for _, forbidden := range []string{
		"example/sidecar:v1",
		"example/sidecar:v2",
		"left-ca",
		"right-ca",
		"replicas: 1",
		"replicas: 2",
		"resources: [\"pods\"]",
		"resources: [\"services\"]",
	} {
		if strings.Contains(combinedDiff, forbidden) {
			t.Fatalf("combined diff includes ignored value %q:\n%s", forbidden, combinedDiff)
		}
	}
}

func TestOrchestratorDiffAppsHonorsSplitGlobalResourceCustomizationJSONPointers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeWebhookApp(t, left, "left-ca")
	writeWebhookApp(t, right, "right-ca")
	writeGlobalCustomization(t, right, `resource.customizations.ignoreDifferences.admissionregistration.k8s.io_MutatingWebhookConfiguration: |
    jsonPointers:
      - /webhooks/0/clientConfig/caBundle
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want caBundle-only diff ignored: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppliesKnownTypeFields(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeBuildApplication(t, left, "rollout", "rollout")
	writeBuildApplication(t, right, "rollout", "rollout")
	writeTestFile(t, filepath.Join(left, "manifests", "rollout", "manifest.yaml"), rolloutWithCPU("0.1"))
	writeTestFile(t, filepath.Join(right, "manifests", "rollout", "manifest.yaml"), rolloutWithCPU("100m"))
	writeGlobalCustomization(t, right, `resource.customizations.knownTypeFields.argoproj.io_Rollout: |
    - field: spec.template.spec
      type: core/v1/PodSpec
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: false,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want no diff after knownTypeFields normalization", result.Results)
	}
}

func TestOrchestratorDiffAppsUnionsApplicationAndGlobalJSONPointers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithReplicasAndAnnotation(t, left, 1, "left", "")
	writeDeploymentAppWithReplicasAndAnnotation(t, right, 2, "right", `  ignoreDifferences:
    - group: apps
      kind: Deployment
      name: demo
      jsonPointers:
        - /spec/replicas
`)
	writeGlobalCustomization(t, right, `resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jsonPointers:
          - /metadata/annotations/generated
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("len(Results) = %d, want app-local and global ignored: %#v", len(result.Results), result.Results)
	}
}

func TestOrchestratorDiffAppsDoesNotWarnForEnforcedGlobalCustomizations(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppWithReplicas(t, left, 1, "")
	writeDeploymentAppWithReplicas(t, right, 1, "")
	writeGlobalCustomization(t, right, `resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jqPathExpressions:
          - .spec.template.metadata.annotations
        managedFieldsManagers:
          - kube-controller-manager
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want none: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffImagesReportsImageChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageApp(t, left, "example/app:v1")
	writeImageApp(t, right, "example/app:v2")

	result, err := Orchestrator{}.DiffImages(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !reflect.DeepEqual(result.Added, []string{"example/app:v2"}) {
		t.Fatalf("Added = %#v, want example/app:v2", result.Added)
	}
	if !reflect.DeepEqual(result.Removed, []string{"example/app:v1"}) {
		t.Fatalf("Removed = %#v, want example/app:v1", result.Removed)
	}
}

func TestOrchestratorDiffImagesReportsUnchangedImage(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageApp(t, left, "example/app:v1")
	writeImageApp(t, right, "example/app:v1")

	result, err := Orchestrator{}.DiffImages(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if len(result.Added) != 0 {
		t.Fatalf("Added = %#v, want empty", result.Added)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("Removed = %#v, want empty", result.Removed)
	}
	if !reflect.DeepEqual(result.Unchanged, []string{"example/app:v1"}) {
		t.Fatalf("Unchanged = %#v, want example/app:v1", result.Unchanged)
	}
}

func TestDiffAppsRefOrigComparesWorkingTreeAgainstBaselineRef(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")
	writeDeploymentAppWithDataValue(t, root, "working")

	result, err := (Orchestrator{}).DiffApps(context.Background(), DiffRequest{
		RightPath:   root,
		Repo:        root,
		RefOrig:     "HEAD",
		ChangedOnly: false,
		Unified:     3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("Results = 0, want baseline-to-working diff")
	}
	diff := diffResultText(result.Results)
	if !strings.Contains(diff, "-  value: baseline") || !strings.Contains(diff, "+  value: working") {
		t.Fatalf("Diff = %q, want baseline-to-working change", diff)
	}
}

func TestDiffAppsRefAndRefOrigCompareCommittedRefs(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")
	checkoutDiffGitBranch(t, wt, "feature")
	writeDeploymentAppWithDataValue(t, root, "feature")
	commitDiffGitRepo(t, repo, wt, "feature")
	writeDeploymentAppWithDataValue(t, root, "uncommitted")

	result, err := (Orchestrator{}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		ChangedOnly: false,
		Unified:     3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("Results = 0, want baseline-to-feature diff")
	}
	diff := diffResultText(result.Results)
	for _, want := range []string{"-  value: baseline", "+  value: feature"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "uncommitted") {
		t.Fatalf("Diff included uncommitted working-tree value:\n%s", diff)
	}
}

func TestDiffAppRefOrigComparesWorkingTreeAgainstBaselineRef(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")
	writeDeploymentAppWithDataValue(t, root, "working")

	result, err := (Orchestrator{}).DiffApp(context.Background(), DiffAppRequest{
		Name: "demo",
		DiffRequest: DiffRequest{
			RightPath:   root,
			Repo:        root,
			RefOrig:     "HEAD",
			ChangedOnly: false,
			Unified:     3,
		},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("Results = 0, want baseline-to-working diff")
	}
	diff := diffResultText(result.Results)
	if !strings.Contains(diff, "-  value: baseline") || !strings.Contains(diff, "+  value: working") {
		t.Fatalf("Diff = %q, want baseline-to-working change", diff)
	}
}

func TestDiffImagesRefOrigComparesWorkingTreeAgainstBaselineRef(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")
	writeDeploymentAppWithDataValue(t, root, "working")

	result, err := (Orchestrator{}).DiffImages(context.Background(), DiffRequest{
		RightPath:   root,
		Repo:        root,
		RefOrig:     "HEAD",
		ChangedOnly: false,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	requireStringInSlice(t, result.Removed, "ghcr.io/example/demo:baseline")
	requireStringInSlice(t, result.Added, "ghcr.io/example/demo:working")
}

func TestDiffAppsRefOrigDoesNotRequirePathOrig(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	_, err := (Orchestrator{}).DiffApps(context.Background(), DiffRequest{
		RightPath:   root,
		Repo:        root,
		RefOrig:     "HEAD",
		ChangedOnly: false,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, want path-orig not required with RefOrig", err)
	}
}

func TestDiffAppsRefValidationErrors(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	tests := []struct {
		name    string
		request DiffRequest
		want    string
	}{
		{
			name:    "repo without refs",
			request: DiffRequest{Repo: root, RightPath: root, LeftPath: root},
			want:    "--repo requires --ref or --ref-orig",
		},
		{
			name:    "path-orig with ref-orig",
			request: DiffRequest{Repo: root, RightPath: root, LeftPath: root, RefOrig: "HEAD"},
			want:    "--ref-orig cannot be combined with --path-orig",
		},
		{
			name:    "missing ref",
			request: DiffRequest{Repo: root, RightPath: root, RefOrig: "missing"},
			want:    "resolve Git ref",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Orchestrator{}).DiffApps(context.Background(), tt.request)
			if err == nil {
				t.Fatalf("DiffApps() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DiffApps() error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDiffAppsRefCleanupOnSecondSnapshotError(t *testing.T) {
	root := t.TempDir()
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	_, err := (Orchestrator{}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "HEAD",
		Ref:         "missing",
		ChangedOnly: false,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want missing right ref error")
	}
	if !strings.Contains(err.Error(), "resolve Git ref") {
		t.Fatalf("DiffApps() error = %q, want missing ref error", err)
	}
	assertNoDrydockGitRefDirs(t, tempRoot)
}

func TestDiffAppsRejectsRemoteCacheInsideEitherRoot(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	_, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:               left,
		RightPath:              right,
		RemoteResourceCacheDir: filepath.Join(right, ".drydock", "remotes"),
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want cache containment error")
	}
	if !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("DiffApps() error = %v, want cache containment error", err)
	}
}

func TestDiffAppsRefRejectsGitCacheInsideOriginalRepo(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	for _, tt := range []struct {
		name    string
		request DiffRequest
	}{
		{
			name: "explicit repo",
			request: DiffRequest{
				Repo:        root,
				RefOrig:     "HEAD",
				Ref:         "HEAD",
				ChangedOnly: false,
				GitCacheDir: filepath.Join(root, ".drydock", "git"),
			},
		},
		{
			name: "repo defaults from right path",
			request: DiffRequest{
				LeftPath:    t.TempDir(),
				RightPath:   root,
				Ref:         "HEAD",
				ChangedOnly: false,
				GitCacheDir: filepath.Join(root, ".drydock", "git"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.request.LeftPath != "" {
				writeDeploymentAppWithDataValue(t, tt.request.LeftPath, "baseline")
			}
			_, err := Orchestrator{}.DiffApps(context.Background(), tt.request)
			if err == nil {
				t.Fatal("DiffApps() error = nil, want git cache containment error")
			}
			if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
				t.Fatalf("DiffApps() error = %v, want original repo git cache containment error", err)
			}
		})
	}
}

func TestDiffAppsRefRejectsRemoteCacheInsideOriginalRepo(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	for _, tt := range []struct {
		name    string
		request DiffRequest
	}{
		{
			name: "explicit repo",
			request: DiffRequest{
				Repo:                   root,
				RefOrig:                "HEAD",
				Ref:                    "HEAD",
				ChangedOnly:            false,
				RemoteResourceCacheDir: filepath.Join(root, ".drydock", "remotes"),
			},
		},
		{
			name: "repo defaults from right path",
			request: DiffRequest{
				LeftPath:               t.TempDir(),
				RightPath:              root,
				Ref:                    "HEAD",
				ChangedOnly:            false,
				RemoteResourceCacheDir: filepath.Join(root, ".drydock", "remotes"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.request.LeftPath != "" {
				writeDeploymentAppWithDataValue(t, tt.request.LeftPath, "baseline")
			}
			_, err := Orchestrator{}.DiffApps(context.Background(), tt.request)
			if err == nil {
				t.Fatal("DiffApps() error = nil, want remote cache containment error")
			}
			if !strings.Contains(err.Error(), "remote resource cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
				t.Fatalf("DiffApps() error = %v, want original repo remote cache containment error", err)
			}
		})
	}
}

func TestOrchestratorDiffAppsPreservesDiagnosticsFromBothPartialBuilds(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeExternalPathApplicationNamed(t, left, "left-broken", "https://github.com/example/left", "manifests/missing-left")
	writeExternalPathApplicationNamed(t, right, "right-broken", "https://github.com/example/right", "manifests/missing-right")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: false,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want partial build error")
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want diagnostics from both sides: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	var sawLeft, sawRight bool
	for _, diag := range result.Diagnostics {
		if diag.Category != "render" {
			t.Fatalf("diagnostic category = %q, want render: %#v", diag.Category, diag)
		}
		sawLeft = sawLeft || strings.Contains(diag.Message, "left-broken")
		sawRight = sawRight || strings.Contains(diag.Message, "right-broken")
	}
	if !sawLeft || !sawRight {
		t.Fatalf("Diagnostics = %#v, want left and right render diagnostics", result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")
	writePluginBuildApplication(t, left, "plugin", "cue")
	writePluginBuildApplication(t, right, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: failingInternalPluginRenderer{}}).DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: false,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want partial plugin render error")
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want successful app diff despite plugin error: %#v", len(result.Results), result.Results)
	}
	if result.Results[0].Parent.Name != "demo" || result.Results[0].Change != "modified" {
		t.Fatalf("Results[0] = %#v, want modified demo diff", result.Results[0])
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestOrchestratorDiffImagesReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageApp(t, left, "example/app:v1")
	writeImageApp(t, right, "example/app:v2")
	writePluginBuildApplication(t, left, "plugin", "cue")
	writePluginBuildApplication(t, right, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: failingInternalPluginRenderer{}}).DiffImages(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: false,
	})
	if err == nil {
		t.Fatal("DiffImages() error = nil, want partial plugin render error")
	}
	if !containsString(result.Removed, "example/app:v1") {
		t.Fatalf("Removed = %#v, want example/app:v1 despite plugin error", result.Removed)
	}
	if !containsString(result.Added, "example/app:v2") {
		t.Fatalf("Added = %#v, want example/app:v2 despite plugin error", result.Added)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsChangedOnlyFallsBackOnUnownedCurrentPath(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")
	writeTestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeTestFile(t, filepath.Join(right, "README.md"), "right\n")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: true,
		Unified:     3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "changed-only" || !strings.Contains(diag.Message, "README.md") {
		t.Fatalf("diagnostic = %#v, want changed-only warning for README.md", diag)
	}

	strictResult, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want strict changed-only error")
	}
	if len(strictResult.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(strictResult.Diagnostics), strictResult.Diagnostics)
	}
	if strictResult.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("strict diagnostic severity = %s, want error", strictResult.Diagnostics[0].Severity)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsApplicationManifestChanges(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppWithDestination(t, left, "old")
	writeSimpleAppWithDestination(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) == 0 {
		t.Fatal("len(Results) = 0, want rendered diff from Application manifest change")
	}
	var sawNewNamespace bool
	for _, item := range result.Results {
		if item.Resource.Namespace == "new" {
			sawNewNamespace = true
		}
	}
	if !sawNewNamespace {
		t.Fatalf("Results = %#v, want diff for new namespace", result.Results)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsDeletedApplicationManifestFromLeft(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeTestFile(t, filepath.Join(right, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: old
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "removed" {
		t.Fatalf("Change = %s, want removed", result.Results[0].Change)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsSameRepoRefHelmValueFile(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeHelmAppWithRefValues(t, left, "old")
	writeHelmAppWithRefValues(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(result.Results[0].Diff, want) {
			t.Fatalf("Diff = %q, want %q", result.Results[0].Diff, want)
		}
	}
}

func TestOrchestratorDiffAppReportsOnlyNamedApplicationChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "demo", "demo", "old")
	writeDiffApplication(t, left, "other", "other", "same")
	writeDiffApplication(t, right, "demo", "demo", "new")
	writeDiffApplication(t, right, "other", "other", "changed-but-not-selected")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name: "demo",
		DiffRequest: DiffRequest{
			LeftPath:    left,
			RightPath:   right,
			Unified:     3,
			ChangedOnly: true,
		},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	got := result.Results[0].Diff
	for _, want := range []string{"Application: argocd/demo", "-  value: old", "+  value: new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
	for _, diffResult := range result.Results {
		if strings.Contains(diffResult.Diff, "other") || strings.Contains(diffResult.Diff, "changed-but-not-selected") {
			t.Fatalf("diff included non-selected Application:\n%s", diffResult.Diff)
		}
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestOrchestratorDiffAppShowsAddedApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeTestFile(t, filepath.Join(left, ".keep"), "left\n")
	writeDiffApplication(t, right, "demo", "demo", "new")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "demo",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 || !strings.Contains(result.Results[0].Diff, "+  value: new") {
		t.Fatalf("DiffApp() result = %#v, want added manifest diff", result.Results)
	}
}

func TestOrchestratorDiffAppShowsDeletedApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "demo", "demo", "old")
	writeTestFile(t, filepath.Join(right, ".keep"), "right\n")

	result, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "demo",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err != nil {
		t.Fatalf("DiffApp() error = %v", err)
	}
	if len(result.Results) == 0 || !strings.Contains(result.Results[0].Diff, "-  value: old") {
		t.Fatalf("DiffApp() result = %#v, want deleted manifest diff", result.Results)
	}
}

func TestOrchestratorDiffAppReportsMissingBothSides(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDiffApplication(t, left, "other", "other", "left")
	writeDiffApplication(t, right, "other", "other", "right")

	_, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name:        "missing",
		DiffRequest: DiffRequest{LeftPath: left, RightPath: right},
	})
	if err == nil {
		t.Fatal("DiffApp() error = nil, want missing error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found in either tree`) {
		t.Fatalf("DiffApp() error = %v, want missing both sides message", err)
	}
}

func TestDiffRequestCarriesProviderFixtureConfig(t *testing.T) {
	request := DiffRequest{
		LeftPath:                       "/tmp/left",
		RightPath:                      "/tmp/right",
		ApplicationSetProviderFixtures: []string{"clusters.yaml"},
		ApplicationSetProviderData:     appset.ProviderData{Clusters: []appset.ClusterInput{{Name: "prod", Server: "https://prod.example.invalid"}}},
	}

	left := request.buildRequest(request.LeftPath, []string{request.LeftPath, request.RightPath})
	right := request.buildRequest(request.RightPath, []string{request.LeftPath, request.RightPath})

	for side, buildRequest := range map[string]BuildRequest{"left": left, "right": right} {
		if !reflect.DeepEqual(buildRequest.ApplicationSetProviderFixtures, request.ApplicationSetProviderFixtures) {
			t.Fatalf("%s ApplicationSetProviderFixtures = %#v, want %#v", side, buildRequest.ApplicationSetProviderFixtures, request.ApplicationSetProviderFixtures)
		}
		if !reflect.DeepEqual(buildRequest.ApplicationSetProviderData, request.ApplicationSetProviderData) {
			t.Fatalf("%s ApplicationSetProviderData = %#v, want %#v", side, buildRequest.ApplicationSetProviderData, request.ApplicationSetProviderData)
		}
	}
}

func TestDiffAppRejectsRemoteCacheInsideEitherRoot(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	for _, root := range []string{left, right} {
		_, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
			Name: "demo",
			DiffRequest: DiffRequest{
				LeftPath:               left,
				RightPath:              right,
				RemoteResourceCacheDir: filepath.Join(root, ".drydock", "remotes"),
			},
		})
		if err == nil {
			t.Fatal("DiffApp() error = nil, want cache containment error")
		}
		if !strings.Contains(err.Error(), "must not be inside repository root") {
			t.Fatalf("DiffApp() error = %v, want cache containment error", err)
		}
	}
}

func TestDiffAppRefRejectsGitCacheInsideOriginalRepo(t *testing.T) {
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	writeDeploymentAppWithDataValue(t, root, "baseline")
	commitDiffGitRepo(t, repo, wt, "baseline")

	_, err := Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{
		Name: "demo",
		DiffRequest: DiffRequest{
			Repo:        root,
			RefOrig:     "HEAD",
			Ref:         "HEAD",
			ChangedOnly: false,
			GitCacheDir: filepath.Join(root, ".drydock", "git"),
		},
	})
	if err == nil {
		t.Fatal("DiffApp() error = nil, want git cache containment error")
	}
	if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("DiffApp() error = %v, want original repo git cache containment error", err)
	}
}

func writeSimpleApp(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
}

func writeSimpleAppWithDestination(t *testing.T, root, namespace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: `+namespace+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: same
`)
}

func writeDeploymentAppWithDataValue(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deploy.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
      - name: demo
        image: ghcr.io/example/demo:`+value+`
`)
}

func writeImageApp(t *testing.T, root, image string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: `+image+`
`)
}

func writeDeploymentAppWithReplicas(t *testing.T, root string, replicas int, ignoreDifferences string) {
	t.Helper()
	writeDeploymentAppWithReplicasAndAnnotation(t, root, replicas, "", ignoreDifferences)
}

func writeDeploymentAppWithReplicasAndAnnotation(t *testing.T, root string, replicas int, annotation, ignoreDifferences string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`+ignoreDifferences)
	annotations := ""
	if annotation != "" {
		annotations = `  annotations:
    generated: ` + annotation + `
`
	}
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
`+annotations+`spec:
  replicas: `+fmt.Sprint(replicas)+`
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: example/app:v1
`)
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func diffResultText(results []diff.Result) string {
	var builder strings.Builder
	for _, result := range results {
		builder.WriteString(result.Diff)
		builder.WriteString("\n")
	}
	return builder.String()
}

func initDiffGitRepo(t *testing.T, root string) (*git.Repository, *git.Worktree) {
	t.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return repo, wt
}

func commitDiffGitRepo(t *testing.T, repo *git.Repository, wt *git.Worktree, message string) plumbing.Hash {
	t.Helper()
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("AddWithOptions() error = %v", err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return hash
}

func checkoutDiffGitBranch(t *testing.T, wt *git.Worktree, name string) {
	t.Helper()
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}); err != nil {
		t.Fatalf("Checkout(%s) error = %v", name, err)
	}
}

func requireStringInSlice(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("values = %#v, want %q", values, want)
}

func assertNoDrydockGitRefDirs(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", root, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "drydock-gitref-") {
			t.Fatalf("temporary Git ref snapshot was not cleaned up: %s", filepath.Join(root, entry.Name()))
		}
	}
}

func writeDeploymentAppWithSidecarImage(t *testing.T, root, sidecarImage, ignoreDifferences string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`+ignoreDifferences)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: example/app:v1
        - name: sidecar
          image: `+sidecarImage+`
`)
}

func writeDeploymentAppWithManagedReplicas(t *testing.T, root string, replicas int, manager, ignoreDifferences string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`+ignoreDifferences)
	managedFields := ""
	if manager != "" {
		managedFields = `  managedFields:
    - apiVersion: apps/v1
      fieldsType: FieldsV1
      fieldsV1:
        f:spec:
          f:replicas: {}
      manager: ` + manager + `
      operation: Update
`
	}
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
`+managedFields+`spec:
  replicas: `+fmt.Sprint(replicas)+`
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: example/app:v1
`)
}

func writeWebhookApp(t *testing.T, root, caBundle string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "webhook.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: webhook
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/webhook
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "webhook", "webhook.yaml"), `apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: demo
webhooks:
  - name: demo.example.com
    clientConfig:
      caBundle: `+caBundle+`
`)
}

func writeConfigMapStatusApp(t *testing.T, root, statusValue string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "status.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: status
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/status
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "status", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: status
status:
  value: `+statusValue+`
`)
}

func writeAggregatedClusterRoleApp(t *testing.T, root, ruleResource string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "role.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: role
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/role
    targetRevision: main
  destination:
    name: in-cluster
`)
	writeTestFile(t, filepath.Join(root, "manifests", "role", "role.yaml"), `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: aggregate-view
aggregationRule:
  clusterRoleSelectors:
    - matchLabels:
        rbac.example.com/aggregate-to-view: "true"
rules:
  - apiGroups: [""]
    resources: ["`+ruleResource+`"]
    verbs: ["get"]
`)
}

func writeCompareOptions(t *testing.T, root, statusMode string, ignoreAggregatedRoles bool) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreResourceStatusField: `+statusMode+`
    ignoreAggregatedRoles: `+fmt.Sprint(ignoreAggregatedRoles)+`
`)
}

func writeGlobalCustomization(t *testing.T, root, data string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  `+data)
}

func rolloutWithCPU(cpu string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: demo-rollout
spec:
  template:
    spec:
      containers:
        - name: app
          image: repo/app:v1
          resources:
            requests:
              cpu: ` + cpu + `
`
}

func writeHelmAppWithRefValues(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo.git
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/values/demo.yaml
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
	writeTestFile(t, filepath.Join(root, "values", "demo.yaml"), `value: `+value+`
`)
}

func writeDiffApplication(t *testing.T, root, appName, configMapName, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  value: `+value+`
`)
}
