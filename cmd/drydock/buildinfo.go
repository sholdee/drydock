package main

import "runtime/debug"

const (
	argoCDModulePath       = "github.com/argoproj/argo-cd/v3"
	gitOpsEngineModulePath = "github.com/argoproj/argo-cd/gitops-engine"
	helmModulePath         = "helm.sh/helm/v4"
	kustomizeModulePath    = "sigs.k8s.io/kustomize/api"
	jsonnetModulePath      = "github.com/google/go-jsonnet"
	kubernetesModulePath   = "k8s.io/apimachinery"
)

func moduleLabel(modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return modulePath
	}
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
