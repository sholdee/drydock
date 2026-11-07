package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
)

// sourceTypeGates holds the argocd-cm keys helm.enable, kustomize.enable,
// and jsonnet.enable as the Argo CD v3.6.1 controller reads them
// (GetEnabledSourceTypes, util/settings/settings.go:1030-1047). The zero
// value enables every tool, as Argo CD does without the keys.
type sourceTypeGates struct {
	helmDisabled      bool
	kustomizeDisabled bool
	jsonnetDisabled   bool
}

func sourceTypeGatesFromSettings(settings config.ArgoSettings) sourceTypeGates {
	return sourceTypeGates{
		helmDisabled:      !settings.SourceTypeEnabled(config.HelmEnableKey),
		kustomizeDisabled: !settings.SourceTypeEnabled(config.KustomizeEnableKey),
		jsonnetDisabled:   !settings.SourceTypeEnabled(config.JsonnetEnableKey),
	}
}

// enabled is discovery.IsManifestGenerationEnabled
// (util/app/discovery/discovery.go:26-35 at v3.6.1) for the source types
// that a key can turn off. Plugin and Directory sources always render;
// jsonnet.enable only decides whether a directory source evaluates its
// .jsonnet files.
func (gates sourceTypeGates) enabled(sourceType argoappv1.ApplicationSourceType) bool {
	switch sourceType {
	case argoappv1.ApplicationSourceTypeHelm:
		return !gates.helmDisabled
	case argoappv1.ApplicationSourceTypeKustomize:
		return !gates.kustomizeDisabled
	case argoappv1.ApplicationSourceTypeDirectory, argoappv1.ApplicationSourceTypePlugin:
		return true
	}
	return true
}

// appliesSourceTypeGates marks localProvider as a sourceTypeGatedProvider.
func (localProvider) appliesSourceTypeGates() {}
