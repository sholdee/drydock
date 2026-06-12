package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/sholdee/drydock/internal/cli"
	"github.com/sholdee/drydock/internal/rendercache"
)

var (
	version = "dev"
	commit  = "none"
)

func moduleLabel(modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return modulePath
	}
	return rendercache.ModuleLabel(info, modulePath)
}

func main() {
	cmd := cli.NewRootCommand(cli.VersionInfo{
		Version:            version,
		Commit:             commit,
		ArgoCDModule:       moduleLabel(rendercache.ArgoCDModulePath),
		GitOpsEngineModule: moduleLabel(rendercache.GitOpsEngineModulePath),
		HelmModule:         moduleLabel(rendercache.HelmModulePath),
		KustomizeModule:    moduleLabel(rendercache.KustomizeModulePath),
		JsonnetModule:      moduleLabel(rendercache.JsonnetModulePath),
		KubernetesModule:   moduleLabel(rendercache.KubernetesModulePath),
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
