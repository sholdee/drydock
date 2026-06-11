package diff

import (
	"fmt"
	"testing"
)

func BenchmarkRunMostlyUnchangedHelmDocuments(b *testing.B) {
	const docs = 500
	left := make([]Document, 0, docs)
	right := make([]Document, 0, docs)
	for i := range docs {
		body := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-%03d
  namespace: default
  labels:
    helm.sh/chart: demo-1.0.0
    app.kubernetes.io/version: "1.0.0"
data:
  value: benchmark
`, i)
		doc := Document{
			Resource: Resource{Kind: "ConfigMap", Name: fmt.Sprintf("demo-%03d", i), Namespace: "default"},
			Body:     body,
		}
		left = append(left, doc)
		right = append(right, doc)
	}
	right[0].Body += "  extra: changed\n"

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		results, err := Run(left, right, Options{})
		if err != nil {
			b.Fatalf("Run() error = %v", err)
		}
		if len(results) != 1 {
			b.Fatalf("results = %d, want 1", len(results))
		}
	}
}
