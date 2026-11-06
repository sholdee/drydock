package diff

import (
	"strings"
	"testing"
)

func TestNormalizeDiffBodyRemovesJQPathExpression(t *testing.T) {
	body := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
        - name: app
          image: example/app:v1
        - name: sidecar
          image: example/sidecar:v1
`

	got, err := normalizeDiffBody(body, Options{}, Normalization{
		ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{`.spec.template.spec.containers[] | select(.name == "sidecar")`}}},
	})
	if err != nil {
		t.Fatalf("normalizeDiffBody() error = %v", err)
	}
	if strings.Contains(got, "sidecar") {
		t.Fatalf("normalized body still contains sidecar:\n%s", got)
	}
}

func TestNormalizeDiffBodyInvalidJQReturnsError(t *testing.T) {
	_, err := normalizeDiffBody("kind: ConfigMap\n", Options{}, Normalization{
		ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{".metadata.name)"}}},
	})
	if err == nil {
		t.Fatal("normalizeDiffBody() error = nil, want invalid jq error")
	}
}

// Argo CD v3.6.1 skips a jq expression that fails while it runs on one
// resource, and keeps the other rules (util/argo/normalizers/
// diff_normalizer.go:210-224). A typical case is a containers expression on
// a ConfigMap, which has no spec.
func TestNormalizeDiffBodySkipsJQPathExpressionThatFailsToRun(t *testing.T) {
	body := `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  ignored: v1
  kept: v1
`
	got, err := normalizeDiffBody(body, Options{}, Normalization{
		ApplicationRules: []IgnoreRule{
			{JQPathExpressions: []string{`.spec.template.spec.containers[] | .image`}},
			{JSONPointers: []string{"/data/ignored"}},
		},
		CustomizationRules: []IgnoreRule{
			{JQPathExpressions: []string{`.data.kept[]`, `.metadata.name`}},
		},
	})
	if err != nil {
		t.Fatalf("normalizeDiffBody() error = %v, want the failed expression skipped", err)
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("normalized body still contains data.ignored:\n%s", got)
	}
	if !strings.Contains(got, "kept: v1") {
		t.Fatalf("normalized body lost data.kept:\n%s", got)
	}
	if strings.Contains(got, "name: demo") {
		t.Fatalf("normalized body still contains metadata.name after a failed expression:\n%s", got)
	}
}
