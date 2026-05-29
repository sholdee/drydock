package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/sholdee/drydock/internal/cli"
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
		JsonnetModule:      moduleLabel(jsonnetModulePath),
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
