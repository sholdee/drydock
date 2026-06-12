package drydock

import (
	"testing"

	"github.com/sholdee/drydock/internal/rendercache"
)

func TestRenderCacheOptionsDefaultEnabled(t *testing.T) {
	client := NewClient(Config{Path: "."})
	options := client.requestOptions()
	if !options.RenderCacheEnabled {
		t.Fatalf("RenderCacheEnabled = false, want true by default (nil Enabled)")
	}
	if options.RenderCacheDir != "" || options.RenderCacheMaxBytes != 0 || options.RefreshRenders {
		t.Fatalf("unexpected non-zero render cache defaults: %+v", options)
	}
}

func TestRenderCacheOptionsExplicit(t *testing.T) {
	disabled := false
	client := NewClient(Config{
		Path: ".",
		RenderCache: RenderCacheOptions{
			Enabled:      &disabled,
			Dir:          "/tmp/render-cache",
			MaxSizeBytes: 1024,
			Refresh:      true,
		},
	})
	options := client.requestOptions()
	if options.RenderCacheEnabled {
		t.Fatalf("RenderCacheEnabled = true, want false")
	}
	if options.RenderCacheDir != "/tmp/render-cache" {
		t.Fatalf("RenderCacheDir = %q, want /tmp/render-cache", options.RenderCacheDir)
	}
	if options.RenderCacheMaxBytes != 1024 {
		t.Fatalf("RenderCacheMaxBytes = %d, want 1024", options.RenderCacheMaxBytes)
	}
	if !options.RefreshRenders {
		t.Fatalf("RefreshRenders = false, want true")
	}
}

func TestRenderCacheFingerprintComputedFromBuildInfo(t *testing.T) {
	client := NewClient(Config{Path: "."})
	options := client.requestOptions()
	want := rendercache.FingerprintFromBuildInfo()
	if options.EngineFingerprint != want {
		t.Fatalf("EngineFingerprint = %#v, want buildinfo-derived %#v", options.EngineFingerprint, want)
	}
}
