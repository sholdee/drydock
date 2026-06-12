package cli

import (
	"fmt"

	"github.com/sholdee/drydock/internal/rendercache"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
)

// quantityFlag parses Kubernetes resource quantities into bytes at flag-parse
// time so invalid cache caps fail before any render work runs.
type quantityFlag struct {
	raw   string
	bytes int64
}

func defaultRenderCacheMaxSize() quantityFlag {
	return quantityFlag{raw: "512Mi", bytes: rendercache.DefaultMaxSizeBytes}
}

func (q *quantityFlag) Set(value string) error {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return fmt.Errorf("invalid quantity %q: %w", value, err)
	}
	bytes := quantity.Value()
	if bytes <= 0 {
		return fmt.Errorf("quantity %q must be positive", value)
	}
	q.raw = value
	q.bytes = bytes
	return nil
}

func (q *quantityFlag) String() string { return q.raw }
func (q *quantityFlag) Type() string   { return "quantity" }

func bindRenderCacheFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.renderCache, "render-cache", flags.renderCache, "reuse persisted Application render outputs across runs")
	cmd.Flags().StringVar(&flags.renderCacheDir, "render-cache-dir", flags.renderCacheDir, "directory for persisted Application render outputs")
	cmd.Flags().Var(&flags.renderCacheMaxSize, "render-cache-max-size", "size cap for the persistent render cache before LRU eviction (Kubernetes quantity, e.g. 512Mi)")
	cmd.Flags().BoolVar(&flags.refreshRenders, "refresh-renders", flags.refreshRenders, "ignore persisted render outputs and overwrite them after rendering")
}

// engineFingerprintFromVersionInfo derives the module list from build info -
// the same source main.go used to populate VersionInfo - and overrides only
// the ldflags-stamped Version and Commit.
func engineFingerprintFromVersionInfo(info VersionInfo) rendercache.EngineFingerprint {
	fingerprint := rendercache.FingerprintFromBuildInfo()
	fingerprint.Version = info.Version
	fingerprint.Commit = info.Commit
	if !fingerprint.Known() {
		if commit, ok := rendercache.VCSCommitFromBuildInfo(); ok {
			fingerprint.Commit = commit
		}
	}
	return fingerprint
}
