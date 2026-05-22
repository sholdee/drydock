package main

import (
	"fmt"
	"os"

	"github.com/home-operations/argocd-local/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	cmd := cli.NewRootCommand(cli.VersionInfo{
		Version:      version,
		Commit:       commit,
		ArgoCDModule: "github.com/argoproj/argo-cd/v3",
	})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
