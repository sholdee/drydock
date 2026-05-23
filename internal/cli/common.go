package cli

import (
	"fmt"
	"strings"

	"github.com/home-operations/argocd-local/internal/source"
	"github.com/spf13/cobra"
)

type commonFlags struct {
	path              string
	pathOrig          string
	repoMaps          []string
	allowNetwork      bool
	offline           bool
	refreshCharts     bool
	chartCacheDir     string
	gitCacheDir       string
	refreshGit        bool
	refreshRemotes    bool
	remoteCacheDir    string
	changedOnly       bool
	strictChangedOnly bool
	strict            bool
	exitCode          bool
	output            string
	unified           int
	limitBytes        int
}

func defaultCommonFlags() commonFlags {
	return commonFlags{
		path:        ".",
		changedOnly: true,
		exitCode:    true,
		output:      "diff",
		unified:     3,
		limitBytes:  65536,
	}
}

func bindCommonFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.path, "path", flags.path, "repository path to inspect")
	cmd.Flags().StringVar(&flags.pathOrig, "path-orig", flags.pathOrig, "baseline repository path for diffs")
	cmd.Flags().StringArrayVar(&flags.repoMaps, "repo-map", flags.repoMaps, "repository URL mapping in from=to form")
	cmd.Flags().BoolVar(&flags.allowNetwork, "allow-network", flags.allowNetwork, "allow network access for unmapped repositories")
	cmd.Flags().BoolVar(&flags.offline, "offline", flags.offline, "disable network access for Helm charts and remote Kustomize resources")
	cmd.Flags().BoolVar(&flags.refreshCharts, "refresh-charts", flags.refreshCharts, "refresh cached Helm charts before rendering")
	cmd.Flags().StringVar(&flags.chartCacheDir, "chart-cache-dir", flags.chartCacheDir, "directory for cached Helm charts")
	cmd.Flags().StringVar(&flags.gitCacheDir, "git-cache-dir", flags.gitCacheDir, "directory for cached Git repositories")
	cmd.Flags().BoolVar(&flags.refreshGit, "refresh-git", flags.refreshGit, "fetch cached Git repositories before rendering")
	cmd.Flags().BoolVar(&flags.refreshRemotes, "refresh-remotes", flags.refreshRemotes, "refresh cached remote Kustomize resources before rendering")
	cmd.Flags().StringVar(&flags.remoteCacheDir, "remote-cache-dir", flags.remoteCacheDir, "directory for cached remote Kustomize resources")
	cmd.Flags().BoolVar(&flags.changedOnly, "changed-only", flags.changedOnly, "limit work to Applications affected by changed files")
	cmd.Flags().BoolVar(&flags.strictChangedOnly, "strict-changed-only", flags.strictChangedOnly, "fail when changed-only input ownership is ambiguous or incomplete")
	cmd.Flags().BoolVar(&flags.strict, "strict", flags.strict, "promote diagnostics to errors")
	cmd.Flags().BoolVar(&flags.exitCode, "exit-code", flags.exitCode, "return exit code 1 when a diff is found")
	cmd.Flags().StringVarP(&flags.output, "output", "o", flags.output, "output format")
	cmd.Flags().IntVarP(&flags.unified, "unified", "u", flags.unified, "number of unified diff context lines")
	cmd.Flags().IntVar(&flags.limitBytes, "limit-bytes", flags.limitBytes, "maximum bytes of rendered output per object")
}

func exitCode(err error, disableDiffExitCode bool, hasDiff bool) int {
	if err != nil {
		return 2
	}
	if hasDiff && !disableDiffExitCode {
		return 1
	}
	return 0
}

func parseRepoMaps(values []string) ([]source.RepoMap, error) {
	out := make([]source.RepoMap, 0, len(values))
	for _, value := range values {
		from, to, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("repo-map %q must use URL=PATH", value)
		}
		out = append(out, source.RepoMap{
			URL:  strings.TrimSpace(from),
			Path: strings.TrimSpace(to),
		})
	}
	return out, nil
}
