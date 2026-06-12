package rendercache

import (
	"runtime/debug"
	"strings"
)

// Render-relevant engine module paths. These participate in the persistent
// key; cmd/drydock and the CLI version output alias them so there is exactly
// one list to extend when a new module starts affecting rendered output.
const (
	ArgoCDModulePath       = "github.com/argoproj/argo-cd/v3"
	GitOpsEngineModulePath = "github.com/argoproj/argo-cd/gitops-engine"
	HelmModulePath         = "helm.sh/helm/v4"
	KustomizeModulePath    = "sigs.k8s.io/kustomize/api"
	JsonnetModulePath      = "github.com/google/go-jsonnet"
	KubernetesModulePath   = "k8s.io/apimachinery"
)

// EngineFingerprint identifies the render code that produced or would
// consume a cache entry. All fields participate in the persistent key.
type EngineFingerprint struct {
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	ArgoCDModule       string `json:"argoCDModule"`
	GitOpsEngineModule string `json:"gitOpsEngineModule"`
	HelmModule         string `json:"helmModule"`
	KustomizeModule    string `json:"kustomizeModule"`
	JsonnetModule      string `json:"jsonnetModule"`
	KubernetesModule   string `json:"kubernetesModule"`
}

// Known reports whether the fingerprint can prove which render code built an
// entry. An un-ldflagged dev build ("none"/empty commit, no clean VCS
// buildinfo) cannot, so persistence is disabled for it.
func (f EngineFingerprint) Known() bool {
	commit := strings.TrimSpace(f.Commit)
	return commit != "" && commit != "none"
}

// FingerprintFromBuildInfo derives a fingerprint from debug.ReadBuildInfo:
// module versions from Deps, commit from clean VCS stamping. pkg/drydock uses
// this so library consumers never supply the fingerprint themselves.
func FingerprintFromBuildInfo() EngineFingerprint {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return EngineFingerprint{Commit: "none"}
	}
	fingerprint := EngineFingerprint{
		Version:            info.Main.Version,
		Commit:             "none",
		ArgoCDModule:       ModuleLabel(info, ArgoCDModulePath),
		GitOpsEngineModule: ModuleLabel(info, GitOpsEngineModulePath),
		HelmModule:         ModuleLabel(info, HelmModulePath),
		KustomizeModule:    ModuleLabel(info, KustomizeModulePath),
		JsonnetModule:      ModuleLabel(info, JsonnetModulePath),
		KubernetesModule:   ModuleLabel(info, KubernetesModulePath),
	}
	if commit, ok := vcsCommit(info); ok {
		fingerprint.Commit = commit
	}
	return fingerprint
}

// VCSCommitFromBuildInfo returns the embedded clean VCS revision, if any. A
// dirty worktree build returns ("", false) because it cannot prove its render
// code matches any cached entry.
func VCSCommitFromBuildInfo() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	return vcsCommit(info)
}

func vcsCommit(info *debug.BuildInfo) (string, bool) {
	revision := ""
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" || dirty {
		return "", false
	}
	return revision, true
}

// ModuleLabel formats the version label for modulePath from build info,
// including any replace directive. Unknown modules return the bare path.
func ModuleLabel(info *debug.BuildInfo, modulePath string) string {
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			return formatModuleLabel(*dep)
		}
	}
	return modulePath
}

func formatModuleLabel(module debug.Module) string {
	label := module.Path
	if module.Version != "" {
		label += "@" + module.Version
	}
	if module.Replace != nil {
		label += " => " + formatModuleLabel(*module.Replace)
	}
	return label
}
