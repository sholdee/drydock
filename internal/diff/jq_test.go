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
		JQPathExpressions: []string{`.spec.template.spec.containers[] | select(.name == "sidecar")`},
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
		JQPathExpressions: []string{".metadata.name)"},
	})
	if err == nil {
		t.Fatal("normalizeDiffBody() error = nil, want invalid jq error")
	}
}
