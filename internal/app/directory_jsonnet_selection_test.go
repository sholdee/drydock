package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestWithSelectionOnlyPathsJsonnetCodeUsesTheControllerNamespace pins
// that changed-only selection substitutes extVar code with the
// ARGOCD_APP_NAME of the controller namespace. An Application in argocd
// with the controller in ops has the instance name argocd_demo, so the
// code imports shared/argocd_demo.libsonnet, not shared/demo.libsonnet.
func TestWithSelectionOnlyPathsJsonnetCodeUsesTheControllerNamespace(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetCodeImportRepo(t, root, "one")
	writeTestFile(t, filepath.Join(root, "shared", "argocd_demo.libsonnet"), "{ value: 'named' }\n")
	side := withSelectionOnlyPathsIn(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, "ops", []ApplicationSelectionInput{{Application: application}})
	if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{"shared/argocd_demo.libsonnet"}); len(selected) != 1 {
		t.Fatalf("change to shared/argocd_demo.libsonnet selected %d Applications, want the Jsonnet source (paths %v)", len(selected), side.inputs[0].Paths)
	}
	if selected, _ := SelectChangedApplicationInputs(side.inputs, []string{"shared/demo.libsonnet"}); len(selected) != 0 {
		t.Fatalf("change to shared/demo.libsonnet selected the Jsonnet source, want nothing in controller namespace ops (paths %v)", side.inputs[0].Paths)
	}
}

// TestWithSelectionOnlyPathsJsonnetCodeWithoutJsonnetFile pins that a
// source without a Jsonnet file is not unknown when its extVar code names
// a revision in an import path: the render never evaluates the code.
func TestWithSelectionOnlyPathsJsonnetCodeWithoutJsonnetFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	application := argoappv1.Application{
		Name:      "demo",
		Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "app", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{Jsonnet: argoappv1.ApplicationSourceJsonnet{
					ExtVars: []argoappv1.JsonnetVar{{Name: "cfg", Value: "import '../shared/${ARGOCD_APP_REVISION_SHORT}.libsonnet'", Code: true}},
				}},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 0 {
		t.Fatalf("unscanned = %v, want nothing for a source without a Jsonnet file", side.unscanned)
	}
}
