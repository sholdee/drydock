package discovery

import (
	"path/filepath"
	"strings"
	"testing"
)

// Argo CD v3.5.3 receives an Application as the JSON kubectl or gitops-engine
// kube.SplitYAML makes of its YAML, parsed as YAML 1.1 by sigs.k8s.io/yaml, so
// an unquoted yes/no/on/off is a boolean before the CRD schema sees it. A
// boolean field accepts it (manifests/crds/application-crd.yaml:237-240,
// directory.recurse); a string field such as a Helm parameter value rejects it
// (:298-300), and so does decoding into the Argo CD v3.5.3 Application type
// ("json: cannot unmarshal bool into Go struct field
// Application.spec.source.helm.parameters.0.value of type string").

func TestScanDecodesYAML11BooleanWordsInBooleanFields(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "apps", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: recurse
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests
    directory:
      recurse: yes
  destination:
    server: https://kubernetes.default.svc
    namespace: recurse
  syncPolicy:
    automated:
      prune: on
`)

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %#v, want one", result.Applications)
	}
	spec := result.Applications[0].Application.Spec
	if spec.Source == nil || spec.Source.Directory == nil || !spec.Source.Directory.Recurse {
		t.Fatalf("spec.source.directory = %#v, want recurse true", spec.Source)
	}
	if spec.SyncPolicy == nil || spec.SyncPolicy.Automated == nil || spec.SyncPolicy.Automated.Prune == nil || !*spec.SyncPolicy.Automated.Prune {
		t.Fatalf("spec.syncPolicy = %#v, want automated prune true", spec.SyncPolicy)
	}
}

func TestScanRejectsYAML11BooleanWordsInStringFields(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "apps", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: params
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: chart
    helm:
      parameters:
        - name: feature.enabled
          value: yes
  destination:
    server: https://kubernetes.default.svc
    namespace: params
`)

	_, err := Scan(root, Options{})
	if err == nil {
		t.Fatal("Scan() error = nil, want a typed decode error for the boolean parameter value")
	}
	for _, want := range []string{"decode Application", "cannot unmarshal bool", "of type string"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Scan() error = %v, want %q", err, want)
		}
	}
}
