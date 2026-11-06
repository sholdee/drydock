package app

import (
	"fmt"

	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/api/resource"
)

// defaultMaxCombinedDirectoryManifestsSize is the default of the Argo CD
// v3.6.1 repo-server flag --max-combined-directory-manifests-size
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:264).
const defaultMaxCombinedDirectoryManifestsSize = "10M"

// maxCombinedDirectoryManifestsSize returns the repo-server limit on the
// combined size of the files of one directory source, in bytes. It reads
// reposerver.max.combined.directory.manifests.size from argocd-cmd-params-cm
// and falls back to the repo-server default. The repo-server parses the
// value with resource.MustParse and takes Quantity.Value, so drydock does the
// same. Zero means no limit.
func maxCombinedDirectoryManifestsSize(settings config.ArgoSettings) (int64, error) {
	value := settings.MaxCombinedDirectoryManifestsSize.Value
	if value == "" {
		value = defaultMaxCombinedDirectoryManifestsSize
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return 0, fmt.Errorf("argocd-cmd-params-cm reposerver.max.combined.directory.manifests.size %q is not a Kubernetes quantity: %w", value, err)
	}
	return quantity.Value(), nil
}
