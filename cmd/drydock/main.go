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
		Version:      version,
		Commit:       commit,
		ArgoCDModule: "github.com/argoproj/argo-cd/v3",
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
