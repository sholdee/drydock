package app

import (
	"fmt"
	"strconv"

	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/api/resource"
)

// defaultMaxCombinedDirectoryManifestsSize is the default of the Argo CD
// v3.6.1 repo-server flag --max-combined-directory-manifests-size
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:264).
const defaultMaxCombinedDirectoryManifestsSize = "10M"

// maxCombinedDirectoryManifestsSize returns the repo-server limit on the
// combined size of the files of one directory source, in bytes. It reads
// reposerver.max.combined.directory.manifests.size from argocd-cmd-params-cm.
// The repo-server reads that key through env.StringFromEnv, which falls back
// to the default 10M when the value is empty (util/env/env.go:155-164). It
// then parses the value with resource.ParseQuantity and stops at startup on
// an error, also for spaces around the value
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:121-122). drydock
// does the same and fails the build instead. Zero means no limit.
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

// maxCombinedDirectoryManifestsSizeSignature is the size limit as the render
// settings signature records it: the byte count, so that "", "10M", and
// "10000000" give the same cache key. A value that does not parse fails the
// build when the provider is built, and is signed as written.
func maxCombinedDirectoryManifestsSizeSignature(settings config.ArgoSettings) string {
	size, err := maxCombinedDirectoryManifestsSize(settings)
	if err != nil {
		return "invalid:" + settings.MaxCombinedDirectoryManifestsSize.Value
	}
	return strconv.FormatInt(size, 10)
}
