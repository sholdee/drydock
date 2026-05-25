package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/spf13/cobra"
)

type diagReport struct {
	Diagnostics []diagnostic.Diagnostic `json:"diagnostics" yaml:"diagnostics"`
	CacheEvents []cacheevent.Event      `json:"cacheEvents,omitempty" yaml:"cacheEvents,omitempty"`
	Settings    *diagSettingsSummary    `json:"settings,omitempty" yaml:"settings,omitempty"`
}

type diagSettingsSummary struct {
	ResourceCustomizations map[string]diagResourceCustomizationSummary `json:"resourceCustomizations,omitempty" yaml:"resourceCustomizations,omitempty"`
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
	includeSettings := false
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Report repository diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, parseErr := parseDiagOutput(flags.output)
			if parseErr != nil {
				return parseErr
			}
			repoMaps, err := parseRepoMaps(flags.repoMaps)
			if err != nil {
				return err
			}
			request := buildRequestFromFlags(flags, repoMaps)
			request.RecordCacheEvents = flags.cacheEvents
			result, err := deps.Orchestrator.Diag(context.Background(), request)
			result.Diagnostics = diagnostic.WithStableCodes(result.Diagnostics)
			report := diagReport{
				Diagnostics: result.Diagnostics,
				CacheEvents: result.CacheEvents,
			}
			if includeSettings {
				report.Settings = settingsSummary(result.Settings)
			}
			switch output {
			case "text":
				if renderErr := renderDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); renderErr != nil {
					return renderErr
				}
			case "json", "yaml":
				if renderErr := writeStructuredOutput(cmd.OutOrStdout(), output, report); renderErr != nil {
					return renderErr
				}
			default:
				return fmt.Errorf("unsupported output %q for diag", output)
			}
			return err
		},
	}
	bindCommonFlags(cmd, &flags)
	cmd.Flags().BoolVar(&includeSettings, "settings", false, "include redacted Argo CD settings summary in structured diagnostic output")
	return cmd
}

func settingsSummary(settings config.ArgoSettings) *diagSettingsSummary {
	summary := &diagSettingsSummary{}
	if len(settings.ResourceCustomizations) > 0 {
		summary.ResourceCustomizations = make(map[string]diagResourceCustomizationSummary, len(settings.ResourceCustomizations))
	}
	for key, customization := range settings.ResourceCustomizations {
		summary.ResourceCustomizations[key] = resourceCustomizationSummary(customization)
	}
	return summary
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
