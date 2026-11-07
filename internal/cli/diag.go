package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/labels"
)

type diagReport struct {
	Diagnostics      []diagnostic.Diagnostic `json:"diagnostics" yaml:"diagnostics"`
	CacheEvents      []cacheevent.Event      `json:"cacheEvents,omitempty" yaml:"cacheEvents,omitempty"`
	PluginExecutions []app.PluginExecution   `json:"pluginExecutions,omitempty" yaml:"pluginExecutions,omitempty"`
	Settings         *diagSettingsSummary    `json:"settings,omitempty" yaml:"settings,omitempty"`
}

type diagSettingsSummary struct {
	ResourceCustomizations map[string]diagResourceCustomizationSummary `json:"resourceCustomizations,omitempty" yaml:"resourceCustomizations,omitempty"`
	CommandParameters      []diagCommandParameterSummary               `json:"commandParameters,omitempty" yaml:"commandParameters,omitempty"`
}

type diagCommandParameterSummary struct {
	Key            string                                `json:"key" yaml:"key"`
	Value          string                                `json:"value,omitempty" yaml:"value,omitempty"`
	ValueRedacted  bool                                  `json:"valueRedacted,omitempty" yaml:"valueRedacted,omitempty"`
	Classification config.CommandParameterClassification `json:"classification,omitempty" yaml:"classification,omitempty"`
	Provenance     diagnostic.Provenance                 `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type diagResourceCustomizationSummary struct {
	HasHealthLua         bool                    `json:"hasHealthLua,omitempty" yaml:"hasHealthLua,omitempty"`
	HealthLuaSHA256      string                  `json:"healthLuaSHA256,omitempty" yaml:"healthLuaSHA256,omitempty"`
	HasIgnoreUpdates     bool                    `json:"hasIgnoreResourceUpdates,omitempty" yaml:"hasIgnoreResourceUpdates,omitempty"`
	HasKnownTypeFields   bool                    `json:"hasKnownTypeFields,omitempty" yaml:"hasKnownTypeFields,omitempty"`
	HasUseOpenLibs       bool                    `json:"hasUseOpenLibs,omitempty" yaml:"hasUseOpenLibs,omitempty"`
	UseOpenLibs          bool                    `json:"useOpenLibs,omitempty" yaml:"useOpenLibs,omitempty"`
	Actions              diagResourceActions     `json:"actions,omitempty" yaml:"actions,omitempty"`
	HasIgnoreDifferences bool                    `json:"hasIgnoreDifferences,omitempty" yaml:"hasIgnoreDifferences,omitempty"`
	KnownTypeFields      []config.KnownTypeField `json:"knownTypeFields,omitempty" yaml:"knownTypeFields,omitempty"`
}

type diagResourceActions struct {
	HasActions          bool                           `json:"hasActions,omitempty" yaml:"hasActions,omitempty"`
	HasDiscoveryLua     bool                           `json:"hasDiscoveryLua,omitempty" yaml:"hasDiscoveryLua,omitempty"`
	DiscoveryLuaSHA256  string                         `json:"discoveryLuaSHA256,omitempty" yaml:"discoveryLuaSHA256,omitempty"`
	ActionNames         []string                       `json:"actionNames,omitempty" yaml:"actionNames,omitempty"`
	ActionLuaSHA256     []config.ResourceActionLuaHash `json:"actionLuaSHA256,omitempty" yaml:"actionLuaSHA256,omitempty"`
	MergeBuiltinActions bool                           `json:"mergeBuiltinActions,omitempty" yaml:"mergeBuiltinActions,omitempty"`
}

func newDiagCommand(info VersionInfo, deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
	flags.parallelism = defaultRenderAppsParallelism()
	flags.engineFingerprint = engineFingerprintFromVersionInfo(info)
	options := diagCommandOptions{}
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Report repository diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDiagCommand(cmd, deps, flags, options)
		},
	}
	bindCommonFlags(cmd, &flags)
	bindSelectorFlag(cmd, &flags)
	cmd.Flags().BoolVar(&options.includeSettings, "settings", false, "include redacted Argo CD settings summary in structured diagnostic output")
	cmd.Flags().BoolVar(&options.includeRender, "render", false, "include render-backed diagnostics by rendering Applications")
	cmd.Flags().BoolVar(&options.includePluginExecutions, "plugin-executions", false, "include plugin execution metadata in structured diagnostic output; renders Applications")
	return cmd
}

type diagCommandOptions struct {
	includeSettings         bool
	includeRender           bool
	includePluginExecutions bool
}

func runDiagCommand(cmd *cobra.Command, deps Dependencies, flags commonFlags, options diagCommandOptions) error {
	output, parseErr := parseDiagOutput(flags.output)
	if parseErr != nil {
		return parseErr
	}
	selector, parseErr := parseApplicationSelector(flags.selector)
	if parseErr != nil {
		return parseErr
	}
	repoMaps, err := parseRepoMaps(flags.repoMaps)
	if err != nil {
		return err
	}
	request := buildRequestFromFlags(cmd, flags, repoMaps)
	if err := rejectBroadDiagPath(request.Path); err != nil {
		return err
	}
	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return err
	}
	request.RecordCacheEvents = flags.cacheEvents
	mode := diagMode{
		Render: options.includeRender || flags.cacheEvents || options.includePluginExecutions,
	}
	result, err := runDiag(context.Background(), deps.Orchestrator, request, mode, applicationSelectorFunc(selector))
	if err == nil {
		result.Diagnostics = append(result.Diagnostics, diagSelectorNoMatchDiagnostics(selector, len(result.Applications), mode)...)
	}
	result.Diagnostics = diagnostic.FilterProjectDiagnostics(diagnostic.WithStableCodes(result.Diagnostics), request.ProjectDiagnosticsMode)
	if err != nil && len(result.Diagnostics) == 0 {
		return err
	}
	report := diagReport{
		Diagnostics: nonNilDiagnostics(result.Diagnostics),
	}
	if flags.cacheEvents {
		report.CacheEvents = result.CacheEvents
	}
	if options.includePluginExecutions {
		report.PluginExecutions = result.PluginExecutions
	}
	if options.includeSettings {
		report.Settings = settingsSummary(result.Settings)
	}
	return renderDiagCommandResult(cmd, output, report, result.Diagnostics, err)
}

func renderDiagCommandResult(cmd *cobra.Command, output string, report diagReport, diagnostics []diagnostic.Diagnostic, runErr error) error {
	switch output {
	case "text":
		if renderErr := renderDiagnostics(cmd.ErrOrStderr(), diagnostics); renderErr != nil {
			return renderErr
		}
		if runErr == nil && len(diagnostics) == 0 {
			if _, renderErr := fmt.Fprintln(cmd.OutOrStdout(), "No diagnostics found."); renderErr != nil {
				return renderErr
			}
		}
	case "json", "yaml":
		if renderErr := writeStructuredOutput(cmd.OutOrStdout(), output, report); renderErr != nil {
			return renderErr
		}
	default:
		return fmt.Errorf("unsupported output %q for diag", output)
	}
	return runErr
}

func rejectBroadDiagPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	// A --path symlink to / or to the home directory scans that tree, so
	// compare the resolved paths too.
	resolved := resolvedDiagPath(abs)
	if filepath.Dir(abs) == abs || filepath.Dir(resolved) == resolved {
		return fmt.Errorf("refusing to recursively scan filesystem root %q; pass --path to a GitOps repository directory", abs)
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		homeAbs, homeErr := filepath.Abs(home)
		if homeErr == nil && (sameCleanPath(abs, homeAbs) || sameCleanPath(resolved, resolvedDiagPath(homeAbs))) {
			return fmt.Errorf("refusing to recursively scan home directory %q; run from a GitOps repository root or pass --path to that repository", abs)
		}
	}
	return nil
}

// resolvedDiagPath returns path with every symlink in it resolved, or path
// when it does not resolve.
func resolvedDiagPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return filepath.Clean(resolved)
}

func sameCleanPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

// diagStaticSelectorHint ends the selector.no-match warning of a diag run
// without rendering: static discovery does not see the child Applications
// that only rendering finds, so the selector may match one of them.
const diagStaticSelectorHint = "; static discovery does not see child Applications that only rendering finds, and --render includes them"

// diagSelectorNoMatchDiagnostics is selectorNoMatchDiagnostics for diag. A
// static run adds diagStaticSelectorHint to the warning.
func diagSelectorNoMatchDiagnostics(selector labels.Selector, selected int, mode diagMode) []diagnostic.Diagnostic {
	diagnostics := selectorNoMatchDiagnostics(selector, selected)
	if !mode.Render {
		for i := range diagnostics {
			diagnostics[i].Message += diagStaticSelectorHint
		}
	}
	return diagnostics
}

type diagMode struct {
	Render bool
}

// runDiag runs the static or the render-backed diagnostics. selectApps, when
// it is not nil, limits the Applications that are validated and rendered.
func runDiag(ctx context.Context, orchestrator Orchestrator, request app.DiagRequest, mode diagMode, selectApps app.ApplicationSelector) (app.DiagResult, error) {
	if mode.Render {
		return orchestrator.Diag(ctx, request, selectApps)
	}
	return orchestrator.DiagStatic(ctx, request, selectApps)
}

func nonNilDiagnostics(diagnostics []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if diagnostics == nil {
		return []diagnostic.Diagnostic{}
	}
	return diagnostics
}

func settingsSummary(settings config.ArgoSettings) *diagSettingsSummary {
	summary := &diagSettingsSummary{}
	if len(settings.ResourceCustomizations) > 0 {
		summary.ResourceCustomizations = make(map[string]diagResourceCustomizationSummary, len(settings.ResourceCustomizations))
	}
	for key, customization := range settings.ResourceCustomizations {
		summary.ResourceCustomizations[key] = resourceCustomizationSummary(customization)
	}
	if len(settings.CommandParameters) > 0 {
		summary.CommandParameters = commandParameterSummary(settings.CommandParameters)
	}
	return summary
}

func commandParameterSummary(parameters []config.CommandParameterSetting) []diagCommandParameterSummary {
	out := make([]diagCommandParameterSummary, 0, len(parameters))
	for _, parameter := range parameters {
		out = append(out, diagCommandParameterSummary{
			Key:            parameter.Key,
			Value:          parameter.Value,
			ValueRedacted:  parameter.ValueRedacted,
			Classification: parameter.Classification,
			Provenance:     parameter.Provenance,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		if out[i].Provenance.Path != out[j].Provenance.Path {
			return out[i].Provenance.Path < out[j].Provenance.Path
		}
		return out[i].Provenance.Pointer < out[j].Provenance.Pointer
	})
	return out
}

func resourceCustomizationSummary(customization config.ResourceCustomization) diagResourceCustomizationSummary {
	return diagResourceCustomizationSummary{
		HasHealthLua:         customization.HasHealthLua,
		HealthLuaSHA256:      customization.HealthLuaSHA256,
		HasIgnoreUpdates:     hasIgnoreDifferences(customization.IgnoreResourceUpdates),
		HasKnownTypeFields:   len(customization.KnownTypeFields) > 0,
		HasUseOpenLibs:       customization.HasUseOpenLibs,
		UseOpenLibs:          customization.UseOpenLibs,
		Actions:              resourceActionsSummary(customization.Actions),
		HasIgnoreDifferences: hasIgnoreDifferences(customization.IgnoreDifferences),
		KnownTypeFields:      append([]config.KnownTypeField(nil), customization.KnownTypeFields...),
	}
}

func resourceActionsSummary(actions config.ResourceActionsSummary) diagResourceActions {
	return diagResourceActions{
		HasActions:          actions.HasActions,
		HasDiscoveryLua:     actions.HasDiscoveryLua,
		DiscoveryLuaSHA256:  actions.DiscoveryLuaSHA256,
		ActionNames:         append([]string(nil), actions.ActionNames...),
		ActionLuaSHA256:     append([]config.ResourceActionLuaHash(nil), actions.ActionLuaSHA256...),
		MergeBuiltinActions: actions.MergeBuiltinActions,
	}
}

func hasIgnoreDifferences(ignore config.OverrideIgnoreDifferences) bool {
	return len(ignore.JSONPointers) > 0 || len(ignore.JQPathExpressions) > 0 || len(ignore.ManagedFieldsManagers) > 0
}

func renderDiagnostics(w io.Writer, diags []diagnostic.Diagnostic) error {
	return renderDiagnosticsWithColor(w, diags, isTerminalWriter(w))
}

func renderDiagnosticsWithColor(w io.Writer, diags []diagnostic.Diagnostic, color bool) error {
	for _, diag := range diagnostic.WithStableCodes(diags) {
		severity := string(diag.Severity)
		if color {
			severity = colorizeDiagnosticSeverity(diag.Severity)
		}
		// The stable code (always set by WithStableCodes) is what -o json, the
		// markdown report, and the docs use to identify a diagnostic, so it
		// replaces the category label. Codes are <category>.<detail> except
		// changed-only (diff.changed-only-incomplete, diff.changed-only-settings,
		// diff.changed-only-settings-scoped, diff.changed-only-projects,
		// diff.changed-only-symlinks, diff.changed-only-jsonnet-scan,
		// diff.changed-only-app-revision),
		// repeated-resource (render.repeated-resource), and crd-scope
		// (build.crd-scope-collision).
		if _, err := fmt.Fprintf(w, "%s %s: %s%s\n", severity, diag.Code, diag.Message, formatDiagnosticProvenance(diag.Provenance)); err != nil {
			return err
		}
	}
	return nil
}

func colorizeDiagnosticSeverity(severity diagnostic.Severity) string {
	switch severity {
	case diagnostic.SeverityWarning:
		return "\x1b[33m" + string(severity) + "\x1b[0m"
	case diagnostic.SeverityError:
		return "\x1b[31m" + string(severity) + "\x1b[0m"
	default:
		return string(severity)
	}
}

func formatDiagnosticProvenance(provenance diagnostic.Provenance) string {
	var parts []string
	if provenance.Path != "" {
		parts = append(parts, "path: "+provenance.Path)
	}
	if provenance.Pointer != "" {
		parts = append(parts, "pointer: "+provenance.Pointer)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
