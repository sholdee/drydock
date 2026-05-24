package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/chart"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

type cacheFlags struct {
	path           string
	pathOrig       string
	gitCacheDir    string
	chartCacheDir  string
	remoteCacheDir string
	source         string
	output         string
	olderThan      string
	dryRun         bool
	yes            bool
	key            string
	all            bool
}

func defaultCacheFlags() cacheFlags {
	return cacheFlags{
		path:   ".",
		output: string(cliformat.OutputTable),
	}
}

func newCacheCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and manage local source caches",
		Args:  cobra.NoArgs,
	}

	pathFlags := defaultCacheFlags()
	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print cache root paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := cacheListOptions(pathFlags)
			if err != nil {
				return err
			}
			if _, err := cache.List(opts); err != nil {
				return err
			}
			return renderCachePaths(cmd, map[cache.Source]string{
				cache.SourceGit:    opts.GitCacheDir,
				cache.SourceChart:  opts.ChartCacheDir,
				cache.SourceRemote: opts.RemoteCacheDir,
			})
		},
	}
	bindCacheRootFlags(pathCmd, &pathFlags)

	listFlags := defaultCacheFlags()
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List cache entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseCacheOutput(listFlags.output)
			if err != nil {
				return err
			}
			opts, err := cacheListOptions(listFlags)
			if err != nil {
				return err
			}
			entries, err := cache.List(opts)
			if err != nil {
				return err
			}
			return renderCacheList(cmd, output, entries)
		},
	}
	bindCacheRootFlags(listCmd, &listFlags)
	bindCacheSourceFlag(listCmd, &listFlags)
	listCmd.Flags().StringVarP(&listFlags.output, "output", "o", listFlags.output, "output format")

	pruneFlags := defaultCacheFlags()
	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stale cache entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseCacheOutput(pruneFlags.output)
			if err != nil {
				return err
			}
			opts, err := cacheOperationOptions(pruneFlags)
			if err != nil {
				return err
			}
			result, err := cache.Prune(opts)
			if err != nil {
				return err
			}
			return renderCacheOperation(cmd, output, result)
		},
	}
	bindCacheRootFlags(pruneCmd, &pruneFlags)
	bindCacheSourceFlag(pruneCmd, &pruneFlags)
	bindCacheOperationFlags(pruneCmd, &pruneFlags)
	pruneCmd.Flags().StringVar(&pruneFlags.olderThan, "older-than", pruneFlags.olderThan, "remove cache entries older than this duration")

	deleteFlags := defaultCacheFlags()
	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete cache entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseCacheOutput(deleteFlags.output)
			if err != nil {
				return err
			}
			if err := validateCacheDeleteFlags(deleteFlags); err != nil {
				return err
			}
			opts, err := cacheOperationOptions(deleteFlags)
			if err != nil {
				return err
			}
			result, err := cache.Delete(opts)
			if err != nil {
				return err
			}
			return renderCacheOperation(cmd, output, result)
		},
	}
	bindCacheRootFlags(deleteCmd, &deleteFlags)
	bindCacheSourceFlag(deleteCmd, &deleteFlags)
	bindCacheOperationFlags(deleteCmd, &deleteFlags)
	deleteCmd.Flags().StringVar(&deleteFlags.key, "key", deleteFlags.key, "cache entry key to delete")
	deleteCmd.Flags().BoolVar(&deleteFlags.all, "all", deleteFlags.all, "delete all selected cache entries")

	cmd.AddCommand(pathCmd, listCmd, pruneCmd, deleteCmd)
	return cmd
}

func bindCacheRootFlags(cmd *cobra.Command, flags *cacheFlags) {
	cmd.Flags().StringVar(&flags.path, "path", flags.path, "repository path used only for cache safety checks")
	cmd.Flags().StringVar(&flags.pathOrig, "path-orig", flags.pathOrig, "baseline repository path used only for cache safety checks")
	cmd.Flags().StringVar(&flags.gitCacheDir, "git-cache-dir", flags.gitCacheDir, "directory for cached Git repositories")
	cmd.Flags().StringVar(&flags.chartCacheDir, "chart-cache-dir", flags.chartCacheDir, "directory for cached Helm charts")
	cmd.Flags().StringVar(&flags.remoteCacheDir, "remote-cache-dir", flags.remoteCacheDir, "directory for cached remote Kustomize resources")
}

func bindCacheSourceFlag(cmd *cobra.Command, flags *cacheFlags) {
	cmd.Flags().StringVar(&flags.source, "source", flags.source, "cache source to select: git, chart, or remote")
}

func bindCacheOperationFlags(cmd *cobra.Command, flags *cacheFlags) {
	cmd.Flags().StringVarP(&flags.output, "output", "o", flags.output, "output format")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", flags.dryRun, "report cache entries that would be removed without deleting them")
	cmd.Flags().BoolVar(&flags.yes, "yes", flags.yes, "confirm destructive cache operation")
}

func cacheListOptions(flags cacheFlags) (cache.Options, error) {
	roots, err := cacheRoots(flags)
	if err != nil {
		return cache.Options{}, err
	}
	sources, err := parseCacheSources(flags.source)
	if err != nil {
		return cache.Options{}, err
	}
	forbiddenRoots, err := cacheForbiddenRoots(flags.path, flags.pathOrig)
	if err != nil {
		return cache.Options{}, err
	}
	return cache.Options{
		GitCacheDir:    roots[cache.SourceGit],
		ChartCacheDir:  roots[cache.SourceChart],
		RemoteCacheDir: roots[cache.SourceRemote],
		Sources:        sources,
		ForbiddenRoots: forbiddenRoots,
	}, nil
}

func cacheOperationOptions(flags cacheFlags) (cache.OperationOptions, error) {
	opts, err := cacheListOptions(flags)
	if err != nil {
		return cache.OperationOptions{}, err
	}
	olderThan := time.Duration(0)
	if strings.TrimSpace(flags.olderThan) != "" {
		olderThan, err = time.ParseDuration(flags.olderThan)
		if err != nil {
			return cache.OperationOptions{}, fmt.Errorf("invalid older-than duration %q: %w", flags.olderThan, err)
		}
	}
	selectedSource := cache.Source("")
	if strings.TrimSpace(flags.source) != "" {
		selectedSource = opts.Sources[0]
	}
	return cache.OperationOptions{
		Options:   opts,
		OlderThan: olderThan,
		DryRun:    flags.dryRun,
		Yes:       flags.yes,
		Source:    selectedSource,
		Key:       strings.TrimSpace(flags.key),
		All:       flags.all,
	}, nil
}

func validateCacheDeleteFlags(flags cacheFlags) error {
	if flags.all && strings.TrimSpace(flags.key) != "" {
		return fmt.Errorf("--all cannot be combined with --key")
	}
	return nil
}

func cacheRoots(flags cacheFlags) (map[cache.Source]string, error) {
	gitCacheDir := flags.gitCacheDir
	if gitCacheDir == "" {
		var err error
		gitCacheDir, err = source.DefaultGitCacheDir()
		if err != nil {
			return nil, err
		}
	}
	chartCacheDir := flags.chartCacheDir
	if chartCacheDir == "" {
		var err error
		chartCacheDir, err = chart.DefaultCacheDir()
		if err != nil {
			return nil, err
		}
	}
	remoteCacheDir := flags.remoteCacheDir
	if remoteCacheDir == "" {
		var err error
		remoteCacheDir, err = remote.DefaultCacheDir()
		if err != nil {
			return nil, err
		}
	}
	return map[cache.Source]string{
		cache.SourceGit:    gitCacheDir,
		cache.SourceChart:  chartCacheDir,
		cache.SourceRemote: remoteCacheDir,
	}, nil
}

func cacheForbiddenRoots(paths ...string) ([]string, error) {
	var roots []string
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			roots = append(roots, path)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	roots = append(roots, cwd)
	return roots, nil
}

func parseCacheSources(raw string) ([]cache.Source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	source := cache.Source(raw)
	switch source {
	case cache.SourceGit, cache.SourceChart, cache.SourceRemote:
		return []cache.Source{source}, nil
	default:
		return nil, fmt.Errorf("unsupported cache source %q", raw)
	}
}

func parseCacheOutput(raw string) (cliformat.Output, error) {
	output := cliformat.Output(strings.TrimSpace(raw))
	if output == "" {
		return cliformat.OutputTable, nil
	}
	switch output {
	case cliformat.OutputTable, cliformat.OutputJSON, cliformat.OutputYAML:
		return output, nil
	default:
		return "", fmt.Errorf("cache output supports table, json, or yaml, got %q", raw)
	}
}

func renderCachePaths(cmd *cobra.Command, roots map[cache.Source]string) error {
	return cliformat.Table(cmd.OutOrStdout(), []cliformat.Column{
		{Header: "SOURCE", Key: "source"},
		{Header: "PATH", Key: "path"},
	}, []map[string]string{
		{"source": string(cache.SourceGit), "path": roots[cache.SourceGit]},
		{"source": string(cache.SourceChart), "path": roots[cache.SourceChart]},
		{"source": string(cache.SourceRemote), "path": roots[cache.SourceRemote]},
	})
}

func renderCacheList(cmd *cobra.Command, output cliformat.Output, entries []cache.Entry) error {
	switch output {
	case cliformat.OutputTable:
		return cliformat.Table(cmd.OutOrStdout(), []cliformat.Column{
			{Header: "SOURCE", Key: "source"},
			{Header: "KIND", Key: "kind"},
			{Header: "KEY", Key: "key"},
			{Header: "SIZE", Key: "size"},
			{Header: "MODIFIED", Key: "modified"},
			{Header: "LEGACY", Key: "legacy"},
			{Header: "PATH", Key: "path"},
		}, cacheEntryRows(entries))
	case cliformat.OutputJSON:
		return cliformat.JSON(cmd.OutOrStdout(), entries)
	case cliformat.OutputYAML:
		return cliformat.YAML(cmd.OutOrStdout(), entries)
	default:
		return fmt.Errorf("unsupported cache output %q", output)
	}
}

func renderCacheOperation(cmd *cobra.Command, output cliformat.Output, result cache.OperationResult) error {
	switch output {
	case cliformat.OutputTable:
		verb := "removed"
		count := result.RemovedCount
		if result.DryRun {
			verb = "would remove"
			count = len(result.Entries)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %d cache entries\n", verb, count)
		return err
	case cliformat.OutputJSON:
		return cliformat.JSON(cmd.OutOrStdout(), result)
	case cliformat.OutputYAML:
		return cliformat.YAML(cmd.OutOrStdout(), result)
	default:
		return fmt.Errorf("unsupported cache output %q", output)
	}
}

func cacheEntryRows(entries []cache.Entry) []map[string]string {
	rows := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, map[string]string{
			"source":   string(entry.Source),
			"kind":     entry.Kind,
			"key":      entry.Key,
			"size":     strconv.FormatInt(entry.SizeBytes, 10),
			"modified": entry.ModifiedAt.UTC().Format(time.RFC3339),
			"legacy":   strconv.FormatBool(entry.Legacy),
			"path":     entry.Path,
		})
	}
	return rows
}
