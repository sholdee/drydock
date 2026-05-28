package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/sholdee/drydock/internal/cli"
)

const (
	argoCDModulePath       = "github.com/argoproj/argo-cd/v3"
	gitOpsEngineModulePath = "github.com/argoproj/argo-cd/gitops-engine"
	helmModulePath         = "helm.sh/helm/v4"
	kustomizeModulePath    = "sigs.k8s.io/kustomize/api"
	kubernetesModulePath   = "k8s.io/apimachinery"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	cmd := cli.NewRootCommand(cli.VersionInfo{
		Version:            version,
		Commit:             commit,
		ArgoCDModule:       moduleLabel(argoCDModulePath),
		GitOpsEngineModule: moduleLabel(gitOpsEngineModulePath),
		HelmModule:         moduleLabel(helmModulePath),
		KustomizeModule:    moduleLabel(kustomizeModulePath),
		KubernetesModule:   moduleLabel(kubernetesModulePath),
	})
	if err := cmd.Execute(); err != nil {
		var exitErr cli.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

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
