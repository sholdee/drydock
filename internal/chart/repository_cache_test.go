package chart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// TestDefaultAcquirerEntryHooks pins the entry hooks: Hold runs on a fetched
// and on a reused entry, and its error fails the acquire. A refresh marks
// the entry refreshed only after it published the fetched chart, and a
// refresh of an entry that the run already refreshed reuses it.
func TestDefaultAcquirerEntryHooks(t *testing.T) {
	server, requests := newRangeIndexServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.Client()}
	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.3", Kind: RepositoryHTTP, VersionPolicy: VersionArgoTargetRevision}
	holdErr := errors.New("hold failed")
	var marked []string
	hooks := &EntryHooks{
		Refreshed:     func(entryPath string) bool { return slices.Contains(marked, entryPath) },
		MarkRefreshed: func(entryPath string) { marked = append(marked, entryPath) },
		Hold:          func(Result) (Result, error) { return Result{}, holdErr },
	}
	for _, refresh := range []bool{true, false} {
		if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Refresh: refresh, EntryHooks: hooks}); !errors.Is(err, holdErr) {
			t.Fatalf("Acquire(refresh %v) error = %v, want the Hold error", refresh, err)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("marked = %v, want the one refreshed entry", marked)
	}
	hooks.Hold = nil
	before := requests["/demo-1.2.3.tgz"]
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Refresh: true, EntryHooks: hooks}); err != nil {
		t.Fatal(err)
	}
	if requests["/demo-1.2.3.tgz"] != before {
		t.Fatalf("archive fetched again after the run refreshed it")
	}

	failing := request
	failing.Name = "missing"
	marked = nil
	if _, err := acquirer.Acquire(context.Background(), failing, Options{CacheDir: cacheDir, Refresh: true, EntryHooks: hooks}); err == nil {
		t.Fatal("Acquire() error = nil, want a failed fetch")
	}
	if len(marked) != 0 {
		t.Fatalf("marked = %v after a failed fetch, want none", marked)
	}
}
