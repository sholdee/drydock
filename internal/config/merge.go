package config

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func MergeDiscovered(candidates []ArgoSettings) (ArgoSettings, []diagnostic.Diagnostic) {
	merged := DefaultSettings()
	diags := make([]diagnostic.Diagnostic, 0, len(candidates))

	for _, candidate := range candidates {
		diags = append(diags, mergeKustomizeBuildOptions(&merged, candidate)...)
		diags = append(diags, mergeTrackingMethod(&merged, candidate)...)
		diags = append(diags, mergeInstanceLabelKey(&merged, candidate)...)
		diags = append(diags, mergeInstallationID(&merged, candidate)...)
		diags = append(diags, mergeHelmValuesFileSchemes(&merged, candidate)...)
		diags = append(diags, mergeHelmRepositories(&merged, candidate)...)
		diags = append(diags, mergeClusters(&merged, candidate)...)
		diags = append(diags, mergeCompareOptions(&merged, candidate)...)
		diags = append(diags, mergeIgnoreResourceUpdatesEnabled(&merged, candidate)...)
		diags = append(diags, mergeInClusterEnabled(&merged, candidate)...)
		diags = append(diags, mergeSourceTypeEnable(&merged, candidate)...)
		diags = append(diags, mergeConfigManagementPlugins(&merged, candidate)...)
		mergeCommandParameters(&merged, candidate)
		diags = append(diags, mergeMaxCombinedDirectoryManifestsSize(&merged, candidate)...)
		diags = append(diags, mergeAllowOutOfBoundsSymlinks(&merged, candidate)...)
		diags = append(diags, mergeEnableProgressiveSyncs(&merged, candidate)...)
		diags = append(diags, mergeIgnoreNormalizerJQTimeout(&merged, candidate)...)
		merged.CommandParametersFound = merged.CommandParametersFound || candidate.CommandParametersFound
		if merged.CommandParametersNamespace == "" {
			merged.CommandParametersNamespace = candidate.CommandParametersNamespace
		}
		diags = append(diags, mergeNamespaceList(&merged.ApplicationNamespaces, candidate.ApplicationNamespaces, applicationNamespacesKey)...)
		diags = append(diags, mergeNamespaceList(&merged.ApplicationSetNamespaces, candidate.ApplicationSetNamespaces, applicationSetNamespacesKey)...)
		merged.ResourceExclusions = append(merged.ResourceExclusions, candidate.ResourceExclusions...)
		merged.ResourceInclusions = append(merged.ResourceInclusions, candidate.ResourceInclusions...)
		merged.ResourceSelectors = append(merged.ResourceSelectors, candidate.ResourceSelectors...)
		diags = append(diags, mergeResourceCustomizations(&merged, candidate)...)
	}

	return merged, diags
}

func mergeKustomizeBuildOptions(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if len(candidate.KustomizeBuildOptions) == 0 {
		return nil
	}
	if len(merged.KustomizeBuildOptions) > 0 && !reflect.DeepEqual(valuesOnly(merged.KustomizeBuildOptions), valuesOnly(candidate.KustomizeBuildOptions)) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			"conflicting kustomize.buildOptions discovered; pass --argocd-cm or --argocd-values",
			firstValueProvenance(candidate.KustomizeBuildOptions),
		)}
	}
	merged.KustomizeBuildOptions = candidate.KustomizeBuildOptions
	return nil
}

func mergeTrackingMethod(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.TrackingMethod) {
		return nil
	}
	if hasProvenance(merged.TrackingMethod) && merged.TrackingMethod.Value != candidate.TrackingMethod.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting application.resourceTrackingMethod values %q and %q", merged.TrackingMethod.Value, candidate.TrackingMethod.Value),
			candidate.TrackingMethod.Provenance,
		)}
	}
	merged.TrackingMethod = candidate.TrackingMethod
	return nil
}

func mergeInstanceLabelKey(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.InstanceLabelKey) {
		return nil
	}
	if hasProvenance(merged.InstanceLabelKey) && merged.InstanceLabelKey.Value != candidate.InstanceLabelKey.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting application.instanceLabelKey values %q and %q", merged.InstanceLabelKey.Value, candidate.InstanceLabelKey.Value),
			candidate.InstanceLabelKey.Provenance,
		)}
	}
	merged.InstanceLabelKey = candidate.InstanceLabelKey
	return nil
}

func mergeInstallationID(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.InstallationID) {
		return nil
	}
	if hasProvenance(merged.InstallationID) && merged.InstallationID.Value != candidate.InstallationID.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting installationID values %q and %q", merged.InstallationID.Value, candidate.InstallationID.Value),
			candidate.InstallationID.Provenance,
		)}
	}
	merged.InstallationID = candidate.InstallationID
	return nil
}

func mergeMaxCombinedDirectoryManifestsSize(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.MaxCombinedDirectoryManifestsSize) {
		return nil
	}
	if hasProvenance(merged.MaxCombinedDirectoryManifestsSize) && merged.MaxCombinedDirectoryManifestsSize.Value != candidate.MaxCombinedDirectoryManifestsSize.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting %s values %q and %q", maxCombinedDirectoryManifestsSizeKey, merged.MaxCombinedDirectoryManifestsSize.Value, candidate.MaxCombinedDirectoryManifestsSize.Value),
			candidate.MaxCombinedDirectoryManifestsSize.Provenance,
		)}
	}
	merged.MaxCombinedDirectoryManifestsSize = candidate.MaxCombinedDirectoryManifestsSize
	return nil
}

// ParseAllowOutOfBoundsSymlinks reads reposerver.allow.oob.symlinks as the
// Argo CD v3.6.1 repo-server reads it, through env.ParseBoolFromEnv with
// the default false.
func ParseAllowOutOfBoundsSymlinks(value string) bool {
	return parseBoolFromEnv(value, false)
}

// ParseEnableProgressiveSyncs reads
// applicationsetcontroller.enable.progressive.syncs as the Argo CD v3.6.1
// ApplicationSet controller reads it, through env.ParseBoolFromEnv with the
// default false.
func ParseEnableProgressiveSyncs(value string) bool {
	return parseBoolFromEnv(value, false)
}

// parseBoolFromEnv copies env.ParseBoolFromEnv (Argo CD v3.6.1
// util/env/env.go:184-193) for a value read from argocd-cmd-params-cm,
// which Kubernetes passes to the process as an environment variable
// unchanged: "true" or "false" in any case, and defaultValue for an empty
// or any other value, such as "1" or " true".
func parseBoolFromEnv(value string, defaultValue bool) bool {
	if value != "" {
		if strings.EqualFold(value, "true") {
			return true
		} else if strings.EqualFold(value, "false") {
			return false
		}
	}
	return defaultValue
}

func mergeAllowOutOfBoundsSymlinks(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.AllowOutOfBoundsSymlinks) {
		return nil
	}
	// Spellings that the repo-server reads the same, such as "TRUE" and
	// "true", do not conflict.
	if hasProvenance(merged.AllowOutOfBoundsSymlinks) && ParseAllowOutOfBoundsSymlinks(merged.AllowOutOfBoundsSymlinks.Value) != ParseAllowOutOfBoundsSymlinks(candidate.AllowOutOfBoundsSymlinks.Value) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting %s values %q and %q", allowOutOfBoundsSymlinksKey, merged.AllowOutOfBoundsSymlinks.Value, candidate.AllowOutOfBoundsSymlinks.Value),
			candidate.AllowOutOfBoundsSymlinks.Provenance,
		)}
	}
	merged.AllowOutOfBoundsSymlinks = candidate.AllowOutOfBoundsSymlinks
	return nil
}

func mergeEnableProgressiveSyncs(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.EnableProgressiveSyncs) {
		return nil
	}
	// Spellings that the controller reads the same do not conflict.
	if hasProvenance(merged.EnableProgressiveSyncs) && ParseEnableProgressiveSyncs(merged.EnableProgressiveSyncs.Value) != ParseEnableProgressiveSyncs(candidate.EnableProgressiveSyncs.Value) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting %s values %q and %q", enableProgressiveSyncsKey, merged.EnableProgressiveSyncs.Value, candidate.EnableProgressiveSyncs.Value),
			candidate.EnableProgressiveSyncs.Provenance,
		)}
	}
	merged.EnableProgressiveSyncs = candidate.EnableProgressiveSyncs
	return nil
}

// DefaultIgnoreNormalizerJQTimeout is Argo CD v3.6.1's
// normalizers.DefaultJQExecutionTimeout. The controller uses it when
// controller.ignore.normalizer.jq.timeout gives zero
// (util/argo/normalizers/diff_normalizer.go:113-118).
const DefaultIgnoreNormalizerJQTimeout = time.Second

// jqTimeoutDurationRegex is the format of github.com/argoproj/pkg/v2/time
// ParseDuration, which env.ParseDurationFromEnv tries after
// time.ParseDuration fails.
var jqTimeoutDurationRegex = regexp.MustCompile(`^(\d+)([smhd])$`)

// ParseIgnoreNormalizerJQTimeout returns the jq timeout that the Argo CD
// v3.6.1 application controller uses for controller.ignore.normalizer.jq.timeout.
// The controller reads the value with env.ParseDurationFromEnv(...,
// 0, 0, math.MaxInt64) (util/env/env.go:123-148), and a zero result means
// DefaultIgnoreNormalizerJQTimeout. So an empty value, a value that does not
// parse, and a negative value all give the default. A value that does not
// parse with time.ParseDuration is tried as a whole number of seconds,
// minutes, hours or days, such as "2d". When that number overflows int64,
// the upstream helper ends the controller with log.Fatal. drydock cannot
// copy that and uses the default instead.
func ParseIgnoreNormalizerJQTimeout(value string) time.Duration {
	timeout := parseDurationLikeArgoCDEnv(value)
	if timeout == 0 {
		return DefaultIgnoreNormalizerJQTimeout
	}
	return timeout
}

func parseDurationLikeArgoCDEnv(value string) time.Duration {
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		matches := jqTimeoutDurationRegex.FindStringSubmatch(value)
		if len(matches) != 3 {
			return 0
		}
		amount, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return 0
		}
		var unit time.Duration
		switch matches[2] {
		case "s":
			unit = time.Second
		case "m":
			unit = time.Minute
		case "h":
			unit = time.Hour
		case "d":
			unit = time.Hour * 24
		}
		duration = unit * time.Duration(amount)
	}
	// The minimum is 0 and the maximum math.MaxInt64, so only a negative
	// value is out of range.
	if duration < 0 {
		return 0
	}
	return duration
}

func mergeIgnoreNormalizerJQTimeout(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenance(candidate.IgnoreNormalizerJQTimeout) {
		return nil
	}
	// Spellings that the controller reads the same, such as "60s" and "1m",
	// do not conflict.
	if hasProvenance(merged.IgnoreNormalizerJQTimeout) && ParseIgnoreNormalizerJQTimeout(merged.IgnoreNormalizerJQTimeout.Value) != ParseIgnoreNormalizerJQTimeout(candidate.IgnoreNormalizerJQTimeout.Value) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting %s values %q and %q", ignoreNormalizerJQTimeoutKey, merged.IgnoreNormalizerJQTimeout.Value, candidate.IgnoreNormalizerJQTimeout.Value),
			candidate.IgnoreNormalizerJQTimeout.Provenance,
		)}
	}
	merged.IgnoreNormalizerJQTimeout = candidate.IgnoreNormalizerJQTimeout
	return nil
}

func mergeHelmValuesFileSchemes(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !candidate.HelmValuesFileSchemesSet {
		return nil
	}
	if merged.HelmValuesFileSchemesSet && !reflect.DeepEqual(valuesOnly(merged.HelmValuesFileSchemes), valuesOnly(candidate.HelmValuesFileSchemes)) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			"conflicting helm.valuesFileSchemes discovered; pass --argocd-cm or --argocd-values",
			candidate.HelmValuesFileSchemesSource,
		)}
	}
	merged.HelmValuesFileSchemes = candidate.HelmValuesFileSchemes
	merged.HelmValuesFileSchemesSet = true
	merged.HelmValuesFileSchemesSource = candidate.HelmValuesFileSchemesSource
	return nil
}

func mergeHelmRepositories(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for url, repo := range candidate.HelmRepositories {
		existing, ok := merged.HelmRepositories[url]
		if ok && !sameRepositorySettings(existing, repo) {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting repository settings discovered for %q", url),
				repo.Provenance,
			))
			continue
		}
		merged.HelmRepositories[url] = repo
	}
	return diags
}

func mergeClusters(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for server, cluster := range candidate.Clusters {
		existing, ok := merged.Clusters[server]
		if ok && !sameClusterSettings(existing, cluster) {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting cluster settings discovered for %q", server),
				cluster.Provenance,
			))
			continue
		}
		merged.Clusters[server] = cluster
	}
	return diags
}

func mergeCompareOptions(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenanceCompareOptions(candidate.CompareOptions) {
		return nil
	}
	if hasProvenanceCompareOptions(merged.CompareOptions) && !sameCompareOptions(merged.CompareOptions, candidate.CompareOptions) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			"conflicting resource.compareoptions settings discovered",
			candidate.CompareOptions.Provenance,
		)}
	}
	if !hasProvenanceCompareOptions(merged.CompareOptions) {
		merged.CompareOptions = candidate.CompareOptions
	}
	return nil
}

func mergeIgnoreResourceUpdatesEnabled(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenanceBool(candidate.IgnoreResourceUpdatesEnabled) {
		return nil
	}
	if hasProvenanceBool(merged.IgnoreResourceUpdatesEnabled) && merged.IgnoreResourceUpdatesEnabled.Value != candidate.IgnoreResourceUpdatesEnabled.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting resource.ignoreResourceUpdatesEnabled values %t and %t", merged.IgnoreResourceUpdatesEnabled.Value, candidate.IgnoreResourceUpdatesEnabled.Value),
			candidate.IgnoreResourceUpdatesEnabled.Provenance,
		)}
	}
	if !hasProvenanceBool(merged.IgnoreResourceUpdatesEnabled) {
		merged.IgnoreResourceUpdatesEnabled = candidate.IgnoreResourceUpdatesEnabled
	}
	return nil
}

func mergeInClusterEnabled(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	if !hasProvenanceBool(candidate.InClusterEnabled) {
		return nil
	}
	if hasProvenanceBool(merged.InClusterEnabled) && merged.InClusterEnabled.Value != candidate.InClusterEnabled.Value {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting cluster.inClusterEnabled values %t and %t", merged.InClusterEnabled.Value, candidate.InClusterEnabled.Value),
			candidate.InClusterEnabled.Provenance,
		)}
	}
	if !hasProvenanceBool(merged.InClusterEnabled) {
		merged.InClusterEnabled = candidate.InClusterEnabled
	}
	return nil
}

func mergeResourceCustomizations(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for key, customization := range candidate.ResourceCustomizations {
		existing, ok := merged.ResourceCustomizations[key]
		if !ok {
			merged.ResourceCustomizations[key] = customization
			continue
		}
		next, conflict := mergeResourceCustomizationSections(existing, customization)
		if conflict != "" {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting resource customization settings discovered for %q (%s)", key, conflict),
				customization.FieldProvenance(conflict),
			))
			continue
		}
		merged.ResourceCustomizations[key] = next
	}
	return diags
}

func mergeConfigManagementPlugins(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for name, plugin := range candidate.ConfigManagementPlugins {
		if name == "" {
			continue
		}
		existing, ok := merged.ConfigManagementPlugins[name]
		if ok && existing.commandFingerprint != plugin.commandFingerprint {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting config management plugin settings discovered for %q", name),
				plugin.Provenance,
			))
			continue
		}
		merged.ConfigManagementPlugins[name] = plugin
	}
	return diags
}

// mergeNamespaceList merges one application.namespaces or
// applicationsetcontroller.namespaces value. Spellings that the controller
// splits into the same list do not conflict.
func mergeNamespaceList(merged *Value[string], candidate Value[string], key string) []diagnostic.Diagnostic {
	if !hasProvenance(candidate) {
		return nil
	}
	if hasProvenance(*merged) && !reflect.DeepEqual(ParseNamespaceList(merged.Value), ParseNamespaceList(candidate.Value)) {
		return []diagnostic.Diagnostic{conflictDiagnostic(
			fmt.Sprintf("conflicting %s values %q and %q", key, merged.Value, candidate.Value),
			candidate.Provenance,
		)}
	}
	*merged = candidate
	return nil
}

func mergeCommandParameters(merged *ArgoSettings, candidate ArgoSettings) {
	if len(candidate.CommandParameters) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(merged.CommandParameters)+len(candidate.CommandParameters))
	for _, setting := range merged.CommandParameters {
		seen[commandParameterSettingKey(setting)] = struct{}{}
	}
	for _, setting := range candidate.CommandParameters {
		key := commandParameterSettingKey(setting)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged.CommandParameters = append(merged.CommandParameters, setting)
	}
}

func commandParameterSettingKey(setting CommandParameterSetting) string {
	return fmt.Sprintf("%s\x00%s\x00%t\x00%s\x00%s\x00%s",
		setting.Key,
		setting.Value,
		setting.ValueRedacted,
		setting.Classification,
		setting.Provenance.Path,
		setting.Provenance.Pointer,
	)
}

func valuesOnly(values []Value[string]) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.Value)
	}
	return out
}

func firstValueProvenance(values []Value[string]) diagnostic.Provenance {
	if len(values) == 0 {
		return diagnostic.Provenance{}
	}
	return values[0].Provenance
}

func hasProvenance(value Value[string]) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func hasProvenanceBool(value Value[bool]) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func hasProvenanceCompareOptions(value ResourceCompareOptions) bool {
	return value.Provenance.Path != "" || value.Provenance.Pointer != ""
}

func sameRepositorySettings(left, right RepositorySettings) bool {
	return left.Name == right.Name &&
		left.Type == right.Type &&
		left.URL == right.URL &&
		left.EnableOCI == right.EnableOCI &&
		left.Project == right.Project
}

func sameClusterSettings(left, right ClusterSettings) bool {
	return left.Name == right.Name &&
		left.Server == right.Server &&
		reflect.DeepEqual(left.Namespaces, right.Namespaces) &&
		left.ClusterResources == right.ClusterResources &&
		left.Project == right.Project
}

func sameCompareOptions(left, right ResourceCompareOptions) bool {
	return left.IgnoreAggregatedRoles == right.IgnoreAggregatedRoles &&
		left.IgnoreResourceStatusField == right.IgnoreResourceStatusField
}

func conflictDiagnostic(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}
