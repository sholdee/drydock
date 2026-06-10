package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	cliformat "github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginonboarding"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/labels"
)

const defaultOnboardingPolicyPath = ".drydock/plugins.yaml"

type pluginPolicyInitFlags struct {
	path                  string
	selector              string
	appsetFixtures        []string
	engine                string
	includeUnused         bool
	noComments            bool
	allowMutableImageTags bool
	write                 bool
	output                string
	overwrite             bool
	bootstrapEntrypoints  []string
}

type pluginPolicyDoctorFlags struct {
	path                 string
	selector             string
	appsetFixtures       []string
	output               string
	strict               bool
	enablePlugins        bool
	pluginPolicyPath     string
	pluginPolicyPathSet  bool
	pluginPolicyRef      string
	pluginPolicyRepo     string
	bootstrapEntrypoints []string
}

func newPluginPolicyCommand(deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin-policy",
		Short: "Scaffold and diagnose trusted plugin policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand", cmd.CommandPath())
		},
	}

	initFlags := pluginPolicyInitFlags{path: ".", engine: string(pluginpolicy.EngineContainer)}
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a PluginPolicy scaffold",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginPolicyInit(cmd, deps, initFlags)
		},
	}
	bindPluginPolicyInitFlags(initCmd, &initFlags)

	doctorFlags := pluginPolicyDoctorFlags{path: ".", output: "text"}
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report PluginPolicy readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginPolicyDoctor(cmd, deps, doctorFlags)
		},
	}
	bindPluginPolicyDoctorFlags(doctorCmd, &doctorFlags)

	cmd.AddCommand(initCmd, doctorCmd)
	return cmd
}

func bindPluginPolicyInitFlags(cmd *cobra.Command, flags *pluginPolicyInitFlags) {
	cmd.Flags().StringVar(&flags.path, "path", flags.path, "repository path to inspect")
	cmd.Flags().StringVarP(&flags.selector, "selector", "l", flags.selector, "label selector for Applications")
	cmd.Flags().StringArrayVar(&flags.appsetFixtures, "appset-provider-fixture", flags.appsetFixtures, "local YAML/JSON fixture file for provider-backed ApplicationSet generators")
	cmd.Flags().StringVar(&flags.engine, "engine", flags.engine, "plugin engine to scaffold: container or exec")
	cmd.Flags().BoolVar(&flags.includeUnused, "include-unused", flags.includeUnused, "include discovered CMP descriptors not used by Applications")
	cmd.Flags().BoolVar(&flags.noComments, "no-comments", flags.noComments, "omit explanatory comments from generated policy")
	cmd.Flags().BoolVar(&flags.allowMutableImageTags, "allow-mutable-image-tags", flags.allowMutableImageTags, "emit inferred tag-only container images with allowMutableImageTag")
	cmd.Flags().BoolVar(&flags.write, "write", flags.write, "write the scaffold to .drydock/plugins.yaml")
	cmd.Flags().StringVar(&flags.output, "output", flags.output, "write the scaffold to a repository-relative policy path")
	cmd.Flags().BoolVar(&flags.overwrite, "overwrite", flags.overwrite, "replace an existing policy file")
	cmd.Flags().StringArrayVar(&flags.bootstrapEntrypoints, "bootstrap-entrypoint", flags.bootstrapEntrypoints, "plugin-rendered bootstrap entrypoint in plugin=sourcePath form")
}

func bindPluginPolicyDoctorFlags(cmd *cobra.Command, flags *pluginPolicyDoctorFlags) {
	cmd.Flags().StringVar(&flags.path, "path", flags.path, "repository path to inspect")
	cmd.Flags().StringVarP(&flags.selector, "selector", "l", flags.selector, "label selector for Applications")
	cmd.Flags().StringArrayVar(&flags.appsetFixtures, "appset-provider-fixture", flags.appsetFixtures, "local YAML/JSON fixture file for provider-backed ApplicationSet generators")
	cmd.Flags().StringVarP(&flags.output, "output", "o", flags.output, "output format: text, json, or yaml")
	cmd.Flags().BoolVar(&flags.strict, "strict", flags.strict, "exit non-zero on PluginPolicy readiness failures")
	cmd.Flags().BoolVar(&flags.enablePlugins, "enable-plugins", flags.enablePlugins, "acknowledge trusted exec/container plugin rendering is enabled")
	cmd.Flags().StringVar(&flags.pluginPolicyPath, "plugin-policy-path", flags.pluginPolicyPath, "plugin policy path relative to the selected policy root")
	cmd.Flags().StringVar(&flags.pluginPolicyRef, "plugin-policy-ref", flags.pluginPolicyRef, "Git ref to use as the trusted plugin policy source")
	cmd.Flags().StringVar(&flags.pluginPolicyRepo, "plugin-policy-repo", flags.pluginPolicyRepo, "local Git repository path used to resolve --plugin-policy-ref")
	cmd.Flags().StringArrayVar(&flags.bootstrapEntrypoints, "bootstrap-entrypoint", flags.bootstrapEntrypoints, "plugin-rendered bootstrap entrypoint in plugin=sourcePath form")
}

func runPluginPolicyInit(cmd *cobra.Command, deps Dependencies, flags pluginPolicyInitFlags) error {
	if flags.write && strings.TrimSpace(flags.output) != "" {
		return fmt.Errorf("--write and --output are mutually exclusive")
	}
	report, err := pluginPolicyOnboardingReport(cmd, deps, onboardingReportOptions{
		Path:                 flags.path,
		Selector:             flags.selector,
		AppsetFixtures:       flags.appsetFixtures,
		IncludeUnused:        flags.includeUnused,
		BootstrapEntrypoints: flags.bootstrapEntrypoints,
	})
	if err != nil {
		return err
	}
	data, err := pluginonboarding.Generate(report, pluginonboarding.GenerateOptions{
		Engine:                pluginpolicy.Engine(strings.TrimSpace(flags.engine)),
		Comments:              !flags.noComments,
		AllowMutableImageTags: flags.allowMutableImageTags,
	})
	if err != nil {
		return err
	}
	if flags.write || strings.TrimSpace(flags.output) != "" {
		outputPath := strings.TrimSpace(flags.output)
		if outputPath == "" {
			outputPath = defaultOnboardingPolicyPath
		}
		target, err := writePluginPolicyScaffold(report.Root, outputPath, data, flags.overwrite)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Wrote %s\n", filepath.ToSlash(target))
		return err
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}

func runPluginPolicyDoctor(cmd *cobra.Command, deps Dependencies, flags pluginPolicyDoctorFlags) error {
	output, err := parsePluginPolicyDoctorOutput(flags.output)
	if err != nil {
		return err
	}
	flags.pluginPolicyPathSet = cmd.Flags().Changed("plugin-policy-path")
	policy, trusted, err := loadPluginPolicyForDoctor(cmd.Context(), resultRoot(flags.path), flags)
	if err != nil {
		return err
	}
	report, err := pluginPolicyOnboardingReport(cmd, deps, onboardingReportOptions{
		Path:                 flags.path,
		Selector:             flags.selector,
		AppsetFixtures:       flags.appsetFixtures,
		IncludeUnused:        false,
		BootstrapEntrypoints: flags.bootstrapEntrypoints,
		ExistingPolicy:       policy,
	})
	if err != nil {
		return err
	}
	readiness := pluginonboarding.Readiness(report, policy, pluginonboarding.DoctorOptions{
		EnablePlugins: flags.enablePlugins,
		TrustedPolicy: trusted,
		Strict:        flags.strict,
	})
	if err := renderPluginPolicyDoctor(cmd, output, readiness); err != nil {
		return err
	}
	if flags.strict && readiness.Status == pluginonboarding.StatusFail {
		return fmt.Errorf("plugin policy readiness failed")
	}
	return nil
}

type onboardingReportOptions struct {
	Path                 string
	Selector             string
	AppsetFixtures       []string
	IncludeUnused        bool
	BootstrapEntrypoints []string
	ExistingPolicy       *pluginpolicy.Policy
}

func pluginPolicyOnboardingReport(cmd *cobra.Command, deps Dependencies, opts onboardingReportOptions) (pluginonboarding.Report, error) {
	selector, err := parseApplicationSelector(opts.Selector)
	if err != nil {
		return pluginonboarding.Report{}, err
	}
	hints, err := parseBootstrapEntrypoints(opts.BootstrapEntrypoints)
	if err != nil {
		return pluginonboarding.Report{}, err
	}
	request := app.BuildRequest{
		Path: opts.Path,
		DiscoveryOptions: app.DiscoveryOptions{
			DiscoveryMode:        app.DiscoveryModeStatic,
			MaxDiscoveryDepth:    0,
			MaxDiscoveryDepthSet: true,
		},
		ApplicationSetOptions: app.ApplicationSetOptions{
			ApplicationSetProviderFixtures: append([]string(nil), opts.AppsetFixtures...),
		},
		PluginOptions: app.PluginOptions{
			DisablePluginPolicy: true,
		},
	}
	result, err := deps.Orchestrator.ListApplications(cmd.Context(), request)
	if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
		return pluginonboarding.Report{}, renderErr
	}
	if err != nil {
		return pluginonboarding.Report{}, err
	}
	inputs := onboardingApplicationInputs(result, selector)
	return pluginonboarding.Analyze(resultRoot(opts.Path), inputs, result.Settings, opts.ExistingPolicy, pluginonboarding.AnalyzeOptions{
		IncludeUnused:        opts.IncludeUnused,
		BootstrapEntrypoints: hints,
	})
}

func onboardingApplicationInputs(result app.BuildResult, selector labels.Selector) []pluginonboarding.ApplicationInput {
	inputsByKey := map[string]app.ApplicationSelectionInput{}
	for _, input := range result.ApplicationInputs {
		key := input.Application.Namespace + "/" + input.Application.Name
		inputsByKey[key] = input
	}
	var out []pluginonboarding.ApplicationInput
	for _, application := range result.Applications {
		if selector != nil && !selector.Matches(labels.Set(application.Labels)) {
			continue
		}
		key := application.Namespace + "/" + application.Name
		input := inputsByKey[key]
		out = append(out, pluginonboarding.ApplicationInput{
			Application: application,
			Paths:       append([]string(nil), input.Paths...),
		})
	}
	return out
}

func resultRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		return "."
	}
	return path
}

func parseBootstrapEntrypoints(values []string) ([]pluginonboarding.BootstrapEntrypointHint, error) {
	var out []pluginonboarding.BootstrapEntrypointHint
	for _, value := range values {
		plugin, sourcePath, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(plugin) == "" || strings.TrimSpace(sourcePath) == "" {
			return nil, fmt.Errorf("bootstrap entrypoint %q must use plugin=sourcePath", value)
		}
		out = append(out, pluginonboarding.BootstrapEntrypointHint{
			Plugin:     strings.TrimSpace(plugin),
			SourcePath: strings.TrimSpace(sourcePath),
		})
	}
	return out, nil
}

func parsePluginPolicyDoctorOutput(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "text":
		return "text", nil
	case string(cliformat.OutputJSON):
		return string(cliformat.OutputJSON), nil
	case string(cliformat.OutputYAML):
		return string(cliformat.OutputYAML), nil
	default:
		return "", fmt.Errorf("plugin-policy doctor output supports text, json, or yaml, got %q", value)
	}
}

func renderPluginPolicyDoctor(cmd *cobra.Command, output string, readiness pluginonboarding.ReadinessReport) error {
	switch output {
	case "text":
		return renderPluginPolicyDoctorText(cmd.OutOrStdout(), readiness)
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML):
		return writeStructuredOutput(cmd.OutOrStdout(), output, readiness)
	default:
		return fmt.Errorf("unsupported output %q for plugin-policy doctor", output)
	}
}

func renderPluginPolicyDoctorText(w interface{ Write([]byte) (int, error) }, readiness pluginonboarding.ReadinessReport) error {
	var b strings.Builder
	b.WriteString("Plugin policy readiness: " + readiness.Status + "\n")
	for _, appReadiness := range readiness.Applications {
		fmt.Fprintf(&b, "%s %s/%s\n", appReadiness.Status, appReadiness.Namespace, appReadiness.Name)
		for _, issue := range appReadiness.Issues {
			fmt.Fprintf(&b, "  - %s: %s\n", issue.Code, issue.Message)
		}
	}
	for _, plugin := range readiness.Plugins {
		if len(plugin.Issues) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s plugin %s\n", plugin.Status, plugin.Name)
		for _, issue := range plugin.Issues {
			fmt.Fprintf(&b, "  - %s: %s\n", issue.Code, issue.Message)
		}
	}
	for _, issue := range readiness.Recommendations {
		fmt.Fprintf(&b, "%s %s: %s\n", issue.Status, issue.Code, issue.Message)
	}
	_, err := w.Write([]byte(b.String()))
	return err
}

func loadPluginPolicyForDoctor(ctx context.Context, root string, flags pluginPolicyDoctorFlags) (*pluginpolicy.Policy, bool, error) {
	policyRoot, trusted, cleanup, err := pluginPolicyDoctorRoot(ctx, root, flags)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	relPath, explicit, err := pluginPolicyDoctorPath(flags)
	if err != nil {
		return nil, false, err
	}
	return readPluginPolicyForDoctor(policyRoot, relPath, explicit, trusted)
}

func pluginPolicyDoctorRoot(ctx context.Context, root string, flags pluginPolicyDoctorFlags) (string, bool, func(), error) {
	if strings.TrimSpace(flags.pluginPolicyRef) == "" {
		if strings.TrimSpace(flags.pluginPolicyRepo) != "" {
			return "", false, func() {}, fmt.Errorf("plugin policy repo requires plugin policy ref")
		}
		return root, false, func() {}, nil
	}
	repo := strings.TrimSpace(flags.pluginPolicyRepo)
	if repo == "" {
		repo = root
	}
	snapshot, err := gitref.Snapshot(ctx, gitref.Request{
		Repo:           repo,
		Ref:            flags.pluginPolicyRef,
		ForbiddenRoots: compactPluginPolicyStrings(root, repo),
	})
	if err != nil {
		return "", false, func() {}, fmt.Errorf("load plugin policy ref %q: %w", flags.pluginPolicyRef, err)
	}
	cleanup := func() { _ = snapshot.Cleanup() }
	return snapshot.Path, true, cleanup, nil
}

func pluginPolicyDoctorPath(flags pluginPolicyDoctorFlags) (string, bool, error) {
	relPath := strings.TrimSpace(flags.pluginPolicyPath)
	explicit := flags.pluginPolicyPathSet || relPath != ""
	if explicit && relPath == "" {
		return "", false, fmt.Errorf("plugin policy path must not be empty")
	}
	if relPath == "" {
		relPath = defaultOnboardingPolicyPath
	}
	return relPath, explicit, nil
}

func readPluginPolicyForDoctor(policyRoot, relPath string, explicit, trusted bool) (*pluginpolicy.Policy, bool, error) {
	clean, err := cleanRepoRelativePath(relPath)
	if err != nil {
		return nil, false, fmt.Errorf("plugin policy path %q must be relative to the selected policy root", relPath)
	}
	target := filepath.Join(policyRoot, clean)
	inside, matchedRoot, err := pathsafety.IsInsideAny(target, []string{policyRoot})
	if err != nil {
		return nil, false, fmt.Errorf("validate plugin policy path %q: %w", relPath, err)
	}
	if !inside {
		return nil, false, fmt.Errorf("plugin policy path %q escapes policy root %q", relPath, matchedRoot)
	}
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		if explicit {
			return nil, false, fmt.Errorf("plugin policy %q does not exist", relPath)
		}
		return nil, trusted, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read plugin policy %q: %w", relPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("plugin policy %q must be a regular file", relPath)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, false, fmt.Errorf("read plugin policy %q: %w", relPath, err)
	}
	policy, err := pluginpolicy.Parse(relPath, data)
	if err != nil {
		return nil, false, err
	}
	return &policy, trusted, nil
}

func writePluginPolicyScaffold(root, relPath string, data []byte, overwrite bool) (string, error) {
	clean, target, err := pluginPolicyOutputTarget(root, relPath)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(target)
	parentRel, err := filepath.Rel(root, parent)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinkPathComponents(root, parentRel, true); err != nil {
		return "", err
	}
	if err := validatePluginPolicyOutputTarget(relPath, target, overwrite); err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	if err := rejectSymlinkPathComponents(root, parentRel, false); err != nil {
		return "", err
	}
	if err := atomicWritePluginPolicyFile(target, data); err != nil {
		return "", err
	}
	return clean, nil
}

func pluginPolicyOutputTarget(root, relPath string) (string, string, error) {
	clean, err := cleanRepoRelativePath(relPath)
	if err != nil {
		return "", "", fmt.Errorf("output path %q must be repository-relative", relPath)
	}
	target := filepath.Join(root, clean)
	inside, matchedRoot, err := pathsafety.IsInsideAny(target, []string{root})
	if err != nil {
		return "", "", fmt.Errorf("validate output path %q: %w", relPath, err)
	}
	if !inside {
		return "", "", fmt.Errorf("output path %q escapes repository root %q", relPath, matchedRoot)
	}
	return clean, target, nil
}

func validatePluginPolicyOutputTarget(relPath, target string, overwrite bool) error {
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output path %q is a symlink", relPath)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output path %q must be a regular file", relPath)
		}
		if !overwrite {
			return fmt.Errorf("output path %q already exists; pass --overwrite to replace it", relPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func atomicWritePluginPolicyFile(target string, data []byte) error {
	parent := filepath.Dir(target)
	tmp, err := os.CreateTemp(parent, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Sync()
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

func cleanRepoRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	clean, ok := pathsafety.CleanRelative(filepath.ToSlash(value))
	if !ok || clean == "." || filepath.IsAbs(value) {
		return "", fmt.Errorf("path must be relative and must not escape the repository")
	}
	return filepath.FromSlash(clean), nil
}

func compactPluginPolicyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func rejectSymlinkPathComponents(root, rel string, allowMissing bool) error {
	current := root
	if rel == "." || rel == "" {
		return nil
	}
	for part := range strings.SplitSeq(filepath.Clean(rel), string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output parent path %q is a symlink", filepath.ToSlash(current))
		}
	}
	return nil
}
