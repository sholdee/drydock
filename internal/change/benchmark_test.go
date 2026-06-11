package change

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkDetectLargeIdenticalTrees(b *testing.B) {
	base := b.TempDir()
	current := b.TempDir()
	for i := range 400 {
		rel := filepath.Join(fmt.Sprintf("dir-%02d", i%20), fmt.Sprintf("file-%03d.yaml", i))
		content := fmt.Appendf(nil, "kind: ConfigMap\nmetadata:\n  name: f-%03d\n", i)
		for _, root := range []string{base, current} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, rel), content, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		changed, err := Detect(base, current)
		if err != nil {
			b.Fatal(err)
		}
		if len(changed) != 0 {
			b.Fatalf("changed = %d, want 0", len(changed))
		}
	}
}
