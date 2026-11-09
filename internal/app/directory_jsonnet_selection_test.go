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

// TestWithSelectionOnlyPathsJsonnetCodeEnvPerApplication pins that the
// selection memo keeps the build environment apart for each Application.
// Two Applications share a source path and extVar code, but the code
// imports a file named after ARGOCD_APP_NAME, so each owns its own file.
func TestWithSelectionOnlyPathsJsonnetCodeEnvPerApplication(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "main.jsonnet"), "std.extVar('cfg')\n")
	writeTestFile(t, filepath.Join(root, "shared", "alpha.libsonnet"), "{}\n")
	writeTestFile(t, filepath.Join(root, "shared", "beta.libsonnet"), "{}\n")
	inputs := []ApplicationSelectionInput{}
	for _, name := range []string{"alpha", "beta"} {
		inputs = append(inputs, ApplicationSelectionInput{Application: argoappv1.Application{
			Name:      name,
			Namespace: "argocd",
			Spec: argoappv1.ApplicationSpec{
				Source: &argoappv1.ApplicationSource{
					RepoURL: "https://git.example.test/org/repo.git", Path: "app", TargetRevision: "main",
					Directory: &argoappv1.ApplicationSourceDirectory{Jsonnet: argoappv1.ApplicationSourceJsonnet{
						ExtVars: []argoappv1.JsonnetVar{{Name: "cfg", Value: "import '../shared/${ARGOCD_APP_NAME}.libsonnet'", Code: true}},
					}},
				},
				Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			},
		}})
	}
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, sourceTypeGates{}, inputs)
	for _, name := range []string{"alpha", "beta"} {
		selected, _ := SelectChangedApplicationInputs(side.inputs, []string{"shared/" + name + ".libsonnet"})
		if len(selected) != 1 || selected[0].Name != name {
			t.Errorf("change to shared/%s.libsonnet selected %v, want only %s (paths %v, %v)", name, selected, name, side.inputs[0].Paths, side.inputs[1].Paths)
		}
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
