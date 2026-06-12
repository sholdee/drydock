package main

import (
	"runtime/debug"

	"github.com/sholdee/drydock/internal/rendercache"
)

const (
	argoCDModulePath       = rendercache.ArgoCDModulePath
	gitOpsEngineModulePath = rendercache.GitOpsEngineModulePath
	helmModulePath         = rendercache.HelmModulePath
	kustomizeModulePath    = rendercache.KustomizeModulePath
	jsonnetModulePath      = rendercache.JsonnetModulePath
	kubernetesModulePath   = rendercache.KubernetesModulePath
)

func moduleLabel(modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return modulePath
	}
	return rendercache.ModuleLabel(info, modulePath)
}
