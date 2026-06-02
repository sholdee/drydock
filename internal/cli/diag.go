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

func newDiagCommand(deps Dependencies) *cobra.Command {
	flags := defaultCommonFlags()
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
	repoMaps, err := parseRepoMaps(flags.repoMaps)
	if err != nil {
		return err
	}
	request := buildRequestFromFlags(cmd, flags, repoMaps)
	if err := rejectBroadDiagPath(request.Path); err != nil {
		return err
	}
	request.RecordCacheEvents = flags.cacheEvents
	result, err := runDiag(context.Background(), deps.Orchestrator, request, diagMode{
		Render: options.includeRender || flags.cacheEvents || options.includePluginExecutions,
	})
	result.Diagnostics = diagnostic.WithStableCodes(result.Diagnostics)
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
	if filepath.Dir(abs) == abs {
		return fmt.Errorf("refusing to recursively scan filesystem root %q; pass --path to a GitOps repository directory", abs)
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		homeAbs, homeErr := filepath.Abs(home)
		if homeErr == nil && sameCleanPath(abs, homeAbs) {
			return fmt.Errorf("refusing to recursively scan home directory %q; run from a GitOps repository root or pass --path to that repository", abs)
		}
	}
	return nil
}

func sameCleanPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

type diagMode struct {
	Render bool
}

func runDiag(ctx context.Context, orchestrator Orchestrator, request app.DiagRequest, mode diagMode) (app.DiagResult, error) {
	if mode.Render {
		return orchestrator.Diag(ctx, request)
	}
	request.DiscoveryMode = app.DiscoveryModeStatic
	request.MaxDiscoveryDepth = 0
	request.MaxDiscoveryDepthSet = true
	result, err := orchestrator.ListApplications(ctx, request)
	diagResult := app.DiagResult{
		Applications: result.Applications,
		Diagnostics:  result.Diagnostics,
		Settings:     result.Settings,
		CacheEvents:  result.CacheEvents,
	}
	return diagResult, err
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
		if _, err := fmt.Fprintf(w, "%s %s: %s%s\n", severity, diag.Category, diag.Message, formatDiagnosticProvenance(diag.Provenance)); err != nil {
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
