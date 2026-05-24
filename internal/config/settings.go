package config

import "github.com/home-operations/argocd-local/internal/diagnostic"

type Provenance = diagnostic.Provenance

type Value[T comparable] struct {
	Value      T          `json:"value" yaml:"value"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type RepositorySettings struct {
	Name       string     `json:"name,omitempty" yaml:"name,omitempty"`
	Type       string     `json:"type,omitempty" yaml:"type,omitempty"`
	URL        string     `json:"url,omitempty" yaml:"url,omitempty"`
	EnableOCI  bool       `json:"enableOCI,omitempty" yaml:"enableOCI,omitempty"`
	Project    string     `json:"project,omitempty" yaml:"project,omitempty"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ResourceFilterRule struct {
	APIGroups  []string   `json:"apiGroups,omitempty" yaml:"apiGroups,omitempty"`
	Kinds      []string   `json:"kinds,omitempty" yaml:"kinds,omitempty"`
	Clusters   []string   `json:"clusters,omitempty" yaml:"clusters,omitempty"`
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
	HasActions          bool     `json:"hasActions,omitempty" yaml:"hasActions,omitempty"`
	HasDiscoveryLua     bool     `json:"hasDiscoveryLua,omitempty" yaml:"hasDiscoveryLua,omitempty"`
	ActionNames         []string `json:"actionNames,omitempty" yaml:"actionNames,omitempty"`
	MergeBuiltinActions bool     `json:"mergeBuiltinActions,omitempty" yaml:"mergeBuiltinActions,omitempty"`
}

type ResourceCustomization struct {
	IgnoreDifferences     OverrideIgnoreDifferences `json:"ignoreDifferences,omitempty" yaml:"ignoreDifferences,omitempty"`
	IgnoreResourceUpdates OverrideIgnoreDifferences `json:"ignoreResourceUpdates,omitempty" yaml:"ignoreResourceUpdates,omitempty"`
	KnownTypeFields       []KnownTypeField          `json:"knownTypeFields,omitempty" yaml:"knownTypeFields,omitempty"`
	HasHealthLua          bool                      `json:"hasHealthLua,omitempty" yaml:"hasHealthLua,omitempty"`
	HasUseOpenLibs        bool                      `json:"hasUseOpenLibs,omitempty" yaml:"hasUseOpenLibs,omitempty"`
	UseOpenLibs           bool                      `json:"useOpenLibs,omitempty" yaml:"useOpenLibs,omitempty"`
	Actions               ResourceActionsSummary    `json:"actions,omitempty" yaml:"actions,omitempty"`
	Provenance            Provenance                `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type ArgoSettings struct {
	KustomizeBuildOptions        []Value[string]                  `json:"kustomizeBuildOptions,omitempty" yaml:"kustomizeBuildOptions,omitempty"`
	HelmRepositories             map[string]RepositorySettings    `json:"helmRepositories,omitempty" yaml:"helmRepositories,omitempty"`
	TrackingMethod               Value[string]                    `json:"trackingMethod,omitempty" yaml:"trackingMethod,omitempty"`
	InstanceLabelKey             Value[string]                    `json:"instanceLabelKey,omitempty" yaml:"instanceLabelKey,omitempty"`
	ResourceExclusions           []ResourceFilterRule             `json:"resourceExclusions,omitempty" yaml:"resourceExclusions,omitempty"`
	ResourceInclusions           []ResourceFilterRule             `json:"resourceInclusions,omitempty" yaml:"resourceInclusions,omitempty"`
	CompareOptions               ResourceCompareOptions           `json:"compareOptions,omitempty" yaml:"compareOptions,omitempty"`
	ResourceCustomizations       map[string]ResourceCustomization `json:"resourceCustomizations,omitempty" yaml:"resourceCustomizations,omitempty"`
	IgnoreResourceUpdatesEnabled Value[bool]                      `json:"ignoreResourceUpdatesEnabled,omitempty" yaml:"ignoreResourceUpdatesEnabled,omitempty"`
}

func DefaultSettings() ArgoSettings {
	return ArgoSettings{
		HelmRepositories:       map[string]RepositorySettings{},
		ResourceCustomizations: map[string]ResourceCustomization{},
		CompareOptions: ResourceCompareOptions{
			IgnoreResourceStatusField: "all",
		},
		TrackingMethod: Value[string]{
			Value: "label",
		},
		InstanceLabelKey: Value[string]{
			Value: "app.kubernetes.io/instance",
		},
		IgnoreResourceUpdatesEnabled: Value[bool]{
			Value: true,
		},
	}
}
