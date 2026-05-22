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

type ArgoSettings struct {
	KustomizeBuildOptions []Value[string]               `json:"kustomizeBuildOptions,omitempty" yaml:"kustomizeBuildOptions,omitempty"`
	HelmRepositories      map[string]RepositorySettings `json:"helmRepositories,omitempty" yaml:"helmRepositories,omitempty"`
	TrackingMethod        Value[string]                 `json:"trackingMethod,omitempty" yaml:"trackingMethod,omitempty"`
	InstanceLabelKey      Value[string]                 `json:"instanceLabelKey,omitempty" yaml:"instanceLabelKey,omitempty"`
}

func DefaultSettings() ArgoSettings {
	return ArgoSettings{
		HelmRepositories: map[string]RepositorySettings{},
		TrackingMethod: Value[string]{
			Value: "label",
		},
		InstanceLabelKey: Value[string]{
			Value: "app.kubernetes.io/instance",
		},
	}
}
