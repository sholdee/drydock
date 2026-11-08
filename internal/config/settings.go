package config

import (
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
)

type Provenance = diagnostic.Provenance

type Value[T comparable] struct {
	Value      T          `json:"value" yaml:"value"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type RepositorySettings struct {
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Type      string `json:"type,omitempty" yaml:"type,omitempty"`
	URL       string `json:"url,omitempty" yaml:"url,omitempty"`
	EnableOCI bool   `json:"enableOCI,omitempty" yaml:"enableOCI,omitempty"`
	// EnableLFS is the enableLfs field of a repository Secret. The Argo CD
	// repo-server then replaces Git LFS pointer files with their content
	// in its checkout (util/git/client.go:799-811).
	EnableLFS  bool       `json:"enableLfs,omitempty" yaml:"enableLfs,omitempty"`
	Project    string     `json:"project,omitempty" yaml:"project,omitempty"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ClusterSettings struct {
	Name             string     `json:"name,omitempty" yaml:"name,omitempty"`
	Server           string     `json:"server,omitempty" yaml:"server,omitempty"`
	Namespaces       []string   `json:"namespaces,omitempty" yaml:"namespaces,omitempty"`
	ClusterResources bool       `json:"clusterResources,omitempty" yaml:"clusterResources,omitempty"`
	Project          string     `json:"project,omitempty" yaml:"project,omitempty"`
	Provenance       Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ResourceFilterRule struct {
	APIGroups []string `json:"apiGroups,omitempty" yaml:"apiGroups,omitempty"`
	Kinds     []string `json:"kinds,omitempty" yaml:"kinds,omitempty"`
	Clusters  []string `json:"clusters,omitempty" yaml:"clusters,omitempty"`
	// Selector is the label selector, in its string form, of a
	// resource.selectors rule. Exclusion and inclusion rules ignore it, as
	// Argo CD's FilteredResource does.
	Selector   string     `json:"selector,omitempty" yaml:"selector,omitempty"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ResourceCompareOptions struct {
	IgnoreAggregatedRoles     bool       `json:"ignoreAggregatedRoles,omitempty" yaml:"ignoreAggregatedRoles,omitempty"`
	IgnoreResourceStatusField string     `json:"ignoreResourceStatusField,omitempty" yaml:"ignoreResourceStatusField,omitempty"`
	Provenance                Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type OverrideIgnoreDifferences struct {
	JSONPointers          []string `json:"jsonPointers,omitempty" yaml:"jsonPointers,omitempty"`
	JQPathExpressions     []string `json:"jqPathExpressions,omitempty" yaml:"jqPathExpressions,omitempty"`
	ManagedFieldsManagers []string `json:"managedFieldsManagers,omitempty" yaml:"managedFieldsManagers,omitempty"`
}

type KnownTypeField struct {
	Field string `json:"field,omitempty" yaml:"field,omitempty"`
	Type  string `json:"type,omitempty" yaml:"type,omitempty"`
}

type ResourceActionsSummary struct {
	HasActions          bool                    `json:"hasActions,omitempty" yaml:"hasActions,omitempty"`
	HasDiscoveryLua     bool                    `json:"hasDiscoveryLua,omitempty" yaml:"hasDiscoveryLua,omitempty"`
	DiscoveryLuaSHA256  string                  `json:"discoveryLuaSHA256,omitempty" yaml:"discoveryLuaSHA256,omitempty"`
	ActionNames         []string                `json:"actionNames,omitempty" yaml:"actionNames,omitempty"`
	ActionLuaSHA256     []ResourceActionLuaHash `json:"actionLuaSHA256,omitempty" yaml:"actionLuaSHA256,omitempty"`
	MergeBuiltinActions bool                    `json:"mergeBuiltinActions,omitempty" yaml:"mergeBuiltinActions,omitempty"`
	fingerprint         string
}

type ResourceActionLuaHash struct {
	Name   string `json:"name" yaml:"name"`
	Index  int    `json:"index" yaml:"index"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type ResourceCustomization struct {
	IgnoreDifferences     OverrideIgnoreDifferences `json:"ignoreDifferences,omitempty" yaml:"ignoreDifferences,omitempty"`
	IgnoreResourceUpdates OverrideIgnoreDifferences `json:"ignoreResourceUpdates,omitempty" yaml:"ignoreResourceUpdates,omitempty"`
	KnownTypeFields       []KnownTypeField          `json:"knownTypeFields,omitempty" yaml:"knownTypeFields,omitempty"`
	HasHealthLua          bool                      `json:"hasHealthLua,omitempty" yaml:"hasHealthLua,omitempty"`
	HealthLuaSHA256       string                    `json:"healthLuaSHA256,omitempty" yaml:"healthLuaSHA256,omitempty"`
	HealthLua             string                    `json:"-" yaml:"-"`
	healthLuaFingerprint  string
	HasUseOpenLibs        bool                   `json:"hasUseOpenLibs,omitempty" yaml:"hasUseOpenLibs,omitempty"`
	UseOpenLibs           bool                   `json:"useOpenLibs,omitempty" yaml:"useOpenLibs,omitempty"`
	Actions               ResourceActionsSummary `json:"actions,omitempty" yaml:"actions,omitempty"`
	Provenance            Provenance             `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	// fieldProvenance records, per field, the argocd-cm entry that set the
	// field. The keys are the split key types: health, useOpenLibs,
	// actions, ignoreDifferences, ignoreResourceUpdates, and
	// knownTypeFields.
	fieldProvenance map[string]Provenance
}

// FieldProvenance returns the argocd-cm entry that set one field of the
// customization. field is a split key type, such as health or
// ignoreDifferences. It falls back to Provenance when the field has no
// recorded entry.
func (customization ResourceCustomization) FieldProvenance(field string) Provenance {
	if provenance, ok := customization.fieldProvenance[field]; ok {
		return provenance
	}
	return customization.Provenance
}

// HealthLuaProvenance returns the argocd-cm entry that set the health Lua
// script.
func (customization ResourceCustomization) HealthLuaProvenance() Provenance {
	return customization.FieldProvenance(overrideFieldHealth)
}

// WithoutProvenance returns a copy of the customization with no
// provenance, for comparisons that must not depend on where settings came
// from.
func (customization ResourceCustomization) WithoutProvenance() ResourceCustomization {
	customization.Provenance = Provenance{}
	customization.fieldProvenance = nil
	return customization
}

type ConfigManagementPlugin struct {
	Name               string                          `json:"-" yaml:"-"`
	Version            string                          `json:"-" yaml:"-"`
	GenerateCommand    []string                        `json:"-" yaml:"-"`
	GenerateArgs       []string                        `json:"-" yaml:"-"`
	HasInit            bool                            `json:"-" yaml:"-"`
	Discover           ConfigManagementPluginDiscovery `json:"-" yaml:"-"`
	Provenance         Provenance                      `json:"-" yaml:"-"`
	commandFingerprint string
}

func (plugin ConfigManagementPlugin) EffectiveName() string {
	if plugin.Version == "" {
		return plugin.Name
	}
	return plugin.Name + "-" + plugin.Version
}

type ConfigManagementPluginDiscovery struct {
	FileName    string
	FindGlob    string
	FindCommand []string
	FindArgs    []string
}

type CommandParameterClassification string

const (
	CommandParameterRuntimeOnly   CommandParameterClassification = "runtime-only"
	CommandParameterRuntimeWiring CommandParameterClassification = "runtime-wiring"
	CommandParameterUnknown       CommandParameterClassification = "unknown"
	// CommandParameterRendered marks a command parameter that drydock
	// applies when it renders.
	CommandParameterRendered CommandParameterClassification = "rendered"
	// CommandParameterDiffed marks a command parameter that drydock applies
	// when it diffs.
	CommandParameterDiffed CommandParameterClassification = "diffed"
)

type CommandParameterSetting struct {
	Key            string                         `json:"key" yaml:"key"`
	Value          string                         `json:"value,omitempty" yaml:"value,omitempty"`
	ValueRedacted  bool                           `json:"valueRedacted,omitempty" yaml:"valueRedacted,omitempty"`
	Classification CommandParameterClassification `json:"classification,omitempty" yaml:"classification,omitempty"`
	Provenance     Provenance                     `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ArgoSettings struct {
	KustomizeBuildOptions []Value[string] `json:"kustomizeBuildOptions,omitempty" yaml:"kustomizeBuildOptions,omitempty"`
	// ArgoCDConfigMapFound reports that drydock read an argocd-cm
	// ConfigMap, or the configs.cm of an argo-cd Helm values file that
	// creates one. Only then does KustomizeBuildOptions hold all the flags
	// that the repo-server passes to kustomize build. Without it, drydock
	// cannot tell them (see EffectiveKustomizeBuildOptions).
	ArgoCDConfigMapFound bool `json:"argoCDConfigMapFound,omitempty" yaml:"argoCDConfigMapFound,omitempty"`
	// KustomizeHelmAssumed makes kustomize builds assume --enable-helm
	// even with a found argocd-cm. drydock sets it only for the renders
	// that read the Argo CD settings an install creates
	// (WithKustomizeHelmAssumed).
	KustomizeHelmAssumed bool `json:"-" yaml:"-"`
	// KustomizeVersions holds the argocd-cm keys kustomize.version.<name>
	// and kustomize.path.<name> by name. Argo CD v3.6.1 registers a
	// kustomize binary for each (GetKustomizeSettings,
	// util/settings/settings.go:1346-1390).
	KustomizeVersions            map[string]Value[string]         `json:"kustomizeVersions,omitempty" yaml:"kustomizeVersions,omitempty"`
	HelmRepositories             map[string]RepositorySettings    `json:"helmRepositories,omitempty" yaml:"helmRepositories,omitempty"`
	Clusters                     map[string]ClusterSettings       `json:"clusters,omitempty" yaml:"clusters,omitempty"`
	HelmValuesFileSchemes        []Value[string]                  `json:"helmValuesFileSchemes,omitempty" yaml:"helmValuesFileSchemes,omitempty"`
	HelmValuesFileSchemesSet     bool                             `json:"helmValuesFileSchemesSet,omitempty" yaml:"helmValuesFileSchemesSet,omitempty"`
	HelmValuesFileSchemesSource  Provenance                       `json:"helmValuesFileSchemesSource,omitempty" yaml:"helmValuesFileSchemesSource,omitempty"`
	TrackingMethod               Value[string]                    `json:"trackingMethod,omitempty" yaml:"trackingMethod,omitempty"`
	InstanceLabelKey             Value[string]                    `json:"instanceLabelKey,omitempty" yaml:"instanceLabelKey,omitempty"`
	InstallationID               Value[string]                    `json:"installationID,omitempty" yaml:"installationID,omitempty"`
	ResourceExclusions           []ResourceFilterRule             `json:"resourceExclusions,omitempty" yaml:"resourceExclusions,omitempty"`
	ResourceInclusions           []ResourceFilterRule             `json:"resourceInclusions,omitempty" yaml:"resourceInclusions,omitempty"`
	ResourceSelectors            []ResourceFilterRule             `json:"resourceSelectors,omitempty" yaml:"resourceSelectors,omitempty"`
	CompareOptions               ResourceCompareOptions           `json:"compareOptions,omitempty" yaml:"compareOptions,omitempty"`
	ResourceCustomizations       map[string]ResourceCustomization `json:"resourceCustomizations,omitempty" yaml:"resourceCustomizations,omitempty"`
	IgnoreResourceUpdatesEnabled Value[bool]                      `json:"ignoreResourceUpdatesEnabled,omitempty" yaml:"ignoreResourceUpdatesEnabled,omitempty"`
	InClusterEnabled             Value[bool]                      `json:"inClusterEnabled,omitempty" yaml:"inClusterEnabled,omitempty"`
	// SourceTypeEnable holds the argocd-cm keys helm.enable,
	// kustomize.enable, and jsonnet.enable that are present, as written.
	// See SourceTypeEnabled.
	SourceTypeEnable map[string]Value[string] `json:"sourceTypeEnable,omitempty" yaml:"sourceTypeEnable,omitempty"`
	// RepoServerHelmNamespace is the namespace that helm uses in the
	// repo-server of the Argo CD install that drydock finds, rendered or
	// committed, when no --namespace is given (see RepoServerHelmNamespace).
	// nil without such an install; HelmNamespace then gives
	// DefaultHelmNamespace.
	RepoServerHelmNamespace *Value[string]                    `json:"repoServerHelmNamespace,omitempty" yaml:"repoServerHelmNamespace,omitempty"`
	ConfigManagementPlugins map[string]ConfigManagementPlugin `json:"-" yaml:"-"`
	CommandParameters       []CommandParameterSetting         `json:"commandParameters,omitempty" yaml:"commandParameters,omitempty"`
	// MaxCombinedDirectoryManifestsSize is the argocd-cmd-params-cm key
	// reposerver.max.combined.directory.manifests.size, a Kubernetes
	// quantity. Empty means the repo-server default, 10M.
	MaxCombinedDirectoryManifestsSize Value[string] `json:"maxCombinedDirectoryManifestsSize,omitempty" yaml:"maxCombinedDirectoryManifestsSize,omitempty"`
	// AllowOutOfBoundsSymlinks is the argocd-cmd-params-cm key
	// reposerver.allow.oob.symlinks, as written. The repo-server reads
	// "true" or "false" in any case; any other value means false.
	AllowOutOfBoundsSymlinks Value[string] `json:"allowOutOfBoundsSymlinks,omitempty" yaml:"allowOutOfBoundsSymlinks,omitempty"`
	// EnableProgressiveSyncs is the argocd-cmd-params-cm key
	// applicationsetcontroller.enable.progressive.syncs, as written. The
	// ApplicationSet controller reads "true" or "false" in any case; any
	// other value means false. It decides whether a RollingSync strategy
	// applies, so it changes which Applications an ApplicationSet generates.
	EnableProgressiveSyncs Value[string] `json:"enableProgressiveSyncs,omitempty" yaml:"enableProgressiveSyncs,omitempty"`
	// EnableNewGitFileGlobbing is the argocd-cmd-params-cm key
	// applicationsetcontroller.enable.new.git.file.globbing, as written. The
	// ApplicationSet controller reads "true" or "false" in any case; any
	// other value means false. It chooses how the Git files generator
	// matches its patterns: git pathspecs (false) or doublestar globs (true).
	EnableNewGitFileGlobbing Value[string] `json:"enableNewGitFileGlobbing,omitempty" yaml:"enableNewGitFileGlobbing,omitempty"`
	// IncludeHiddenDirectories is the argocd-cmd-params-cm key
	// reposerver.include.hidden.directories, as written. The repo-server
	// reads "true" or "false" in any case; any other value means false. It
	// decides whether the Git and OCI directory generators see hidden
	// directories.
	IncludeHiddenDirectories Value[string] `json:"includeHiddenDirectories,omitempty" yaml:"includeHiddenDirectories,omitempty"`
	// EnableGitSubmodules is the argocd-cmd-params-cm key
	// reposerver.enable.git.submodule, as written. The repo-server reads
	// "true" or "false" in any case; any other value means true. It
	// decides whether a rendered Git source holds the content of its
	// submodules. See ParseEnableGitSubmodules.
	EnableGitSubmodules Value[string] `json:"enableGitSubmodules,omitempty" yaml:"enableGitSubmodules,omitempty"`
	// AppSetEnableGitSubmodules is the argocd-cmd-params-cm key
	// applicationsetcontroller.enable.git.submodule, as written, read like
	// EnableGitSubmodules. It decides whether the Git generator sees the
	// content of submodules.
	AppSetEnableGitSubmodules Value[string] `json:"appSetEnableGitSubmodules,omitempty" yaml:"appSetEnableGitSubmodules,omitempty"`
	// IgnoreNormalizerJQTimeout is the argocd-cmd-params-cm key
	// controller.ignore.normalizer.jq.timeout, as written. It limits how long
	// one jq ignore-rule deletion may run. See ParseIgnoreNormalizerJQTimeout.
	IgnoreNormalizerJQTimeout Value[string] `json:"ignoreNormalizerJQTimeout,omitempty" yaml:"ignoreNormalizerJQTimeout,omitempty"`
	// CommandParametersFound reports that drydock read an
	// argocd-cmd-params-cm ConfigMap. Only then do ApplicationNamespaces and
	// ApplicationSetNamespaces decide which Applications and
	// ApplicationSets Argo CD reconciles: without the ConfigMap, drydock
	// cannot tell how the cluster sets them.
	CommandParametersFound bool `json:"commandParametersFound,omitempty" yaml:"commandParametersFound,omitempty"`
	// CommandParametersNamespace is the metadata.namespace of the
	// argocd-cmd-params-cm ConfigMap, when it names one. The ConfigMap lives
	// in the Argo CD namespace, which the controllers always reconcile.
	CommandParametersNamespace string `json:"commandParametersNamespace,omitempty" yaml:"commandParametersNamespace,omitempty"`
	// ApplicationNamespaces is the argocd-cmd-params-cm key
	// application.namespaces, as written. The application controller
	// reconciles an Application only in its own namespace or in a namespace
	// that this list matches. See ParseNamespaceList.
	ApplicationNamespaces Value[string] `json:"applicationNamespaces,omitempty" yaml:"applicationNamespaces,omitempty"`
	// ApplicationSetNamespaces is the argocd-cmd-params-cm key
	// applicationsetcontroller.namespaces, as written. The ApplicationSet
	// controller reconciles an ApplicationSet only in its own namespace or in
	// a namespace that this list matches.
	ApplicationSetNamespaces Value[string] `json:"applicationSetNamespaces,omitempty" yaml:"applicationSetNamespaces,omitempty"`
}

// DefaultControllerNamespace is the Argo CD namespace that drydock assumes
// when nothing in the repository names one.
const DefaultControllerNamespace = "argocd"

// ControllerNamespace resolves the namespace that the Argo CD controllers
// run in. The argocd-cmd-params-cm ConfigMap lives there, so its
// metadata.namespace decides when it names one. Otherwise fallback decides,
// such as the namespace of an AppProject, and then
// DefaultControllerNamespace. The namespace gate and AppProject validation
// use this one rule, so they cannot disagree. One known exception remains:
// resource tracking and Application instance names (internal/app
// tracking.go) still assume argocd, as docs/compatibility.md records.
func (s ArgoSettings) ControllerNamespace(fallback string) string {
	if namespace := strings.TrimSpace(s.CommandParametersNamespace); namespace != "" {
		return namespace
	}
	if namespace := strings.TrimSpace(fallback); namespace != "" {
		return namespace
	}
	return DefaultControllerNamespace
}

// ControllerNamespaceKnown reports whether an argocd-cmd-params-cm that
// drydock read names its namespace, so ControllerNamespace does not guess.
func (s ArgoSettings) ControllerNamespaceKnown() bool {
	return s.CommandParametersFound && strings.TrimSpace(s.CommandParametersNamespace) != ""
}

func DefaultSettings() ArgoSettings {
	return ArgoSettings{
		HelmRepositories:        map[string]RepositorySettings{},
		Clusters:                map[string]ClusterSettings{},
		HelmValuesFileSchemes:   valuesFromStrings([]string{"https", "http"}, Provenance{}),
		ResourceCustomizations:  map[string]ResourceCustomization{},
		ConfigManagementPlugins: map[string]ConfigManagementPlugin{},
		CompareOptions: ResourceCompareOptions{
			IgnoreResourceStatusField: "all",
		},
		TrackingMethod: Value[string]{
			Value: "annotation",
		},
		InstanceLabelKey: Value[string]{
			Value: "app.kubernetes.io/instance",
		},
		IgnoreResourceUpdatesEnabled: Value[bool]{
			Value: true,
		},
		// argocd-cm cluster.inClusterEnabled defaults to true: Argo CD serves
		// the built-in in-cluster destination unless it is disabled.
		InClusterEnabled: Value[bool]{
			Value: true,
		},
	}
}

func valuesFromStrings(values []string, provenance Provenance) []Value[string] {
	out := make([]Value[string], 0, len(values))
	for _, value := range values {
		out = append(out, Value[string]{Value: value, Provenance: provenance})
	}
	return out
}
