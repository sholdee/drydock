package diff

import (
	"strings"
	"testing"
)

func TestRunParentAwareDiff(t *testing.T) {
	left := []Document{{
		Parent: Parent{
			Namespace: "argocd",
			Name:      "app-a",
		},
		SourceIndex: 0,
		SourcePath:  "apps/a",
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: old\n",
	}}
	right := []Document{{
		Parent: Parent{
			Namespace: "argocd",
			Name:      "app-a",
		},
		SourceIndex: 0,
		SourcePath:  "apps/a",
		Resource: Resource{
			Kind:      "ConfigMap",
			Namespace: "default",
			Name:      "cfg",
		},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\ndata:\n  value: new\n",
	}}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Change != ChangeModified {
		t.Fatalf("Change = %q, want %q", results[0].Change, ChangeModified)
	}
	for _, want := range []string{
		"Application: argocd/app-a",
		"-  value: old",
		"+  value: new",
	} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

func TestExtractWorkloadImages(t *testing.T) {
	body := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
`

	images, err := ExtractImages(body)
	if err != nil {
		t.Fatalf("ExtractImages() error = %v", err)
	}
	if len(images) != 1 || images[0] != "ghcr.io/example/web:v1" {
		t.Fatalf("ExtractImages() = %#v, want ghcr.io/example/web:v1", images)
	}
}
