package chart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReadyChartEntry writes a cache entry at keyDir whose chart holds
// marker.
func writeReadyChartEntry(t *testing.T, keyDir, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(keyDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "demo", "Chart.yaml"), []byte("name: demo\ndescription: "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readChartEntryMarker(t *testing.T, keyDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(keyDir, "demo", "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, marker, _ := strings.Cut(string(data), "description: ")
	return strings.TrimSpace(marker)
}

// TestPublishChartCacheAcceptsOnlyAnotherWritersEntry pins that a failed
// publish keeps the entry only when another writer made it: the move of the
// fetched entry fails because keyDir now holds that writer's entry. When
// the move fails for another reason, the old entry moves back, and the
// publish fails instead of reporting the old entry as fetched.
func TestPublishChartCacheAcceptsOnlyAnotherWritersEntry(t *testing.T) {
	for _, tt := range []struct {
		name        string
		oldEntry    bool
		otherWriter bool
		wantErr     bool
		wantMarker  string
	}{
		{name: "another writer, no old entry", otherWriter: true, wantMarker: "other"},
		{name: "another writer, old entry", oldEntry: true, otherWriter: true, wantMarker: "other"},
		{name: "failed move, old entry", oldEntry: true, wantErr: true, wantMarker: "old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			keyDir := filepath.Join(parent, "key")
			tmpKeyDir := filepath.Join(parent, ".key.tmp")
			writeReadyChartEntry(t, tmpKeyDir, "fetched")
			if tt.oldEntry {
				writeReadyChartEntry(t, keyDir, "old")
			}
			publishRename = func(from, to string) error {
				if from == tmpKeyDir {
					if !tt.otherWriter {
						return errors.New("rename failed")
					}
					writeReadyChartEntry(t, keyDir, "other")
				}
				return os.Rename(from, to)
			}
			defer func() { publishRename = os.Rename }()
			err := publishChartCache(keyDir, tmpKeyDir, "demo")
			if (err != nil) != tt.wantErr {
				t.Fatalf("publishChartCache() error = %v, want error %v", err, tt.wantErr)
			}
			if got := readChartEntryMarker(t, keyDir); got != tt.wantMarker {
				t.Fatalf("entry = %q, want %q", got, tt.wantMarker)
			}
		})
	}
}
