package app

import (
	"fmt"

	"github.com/argoproj/argo-cd/gitops-engine/pkg/utils/kube"
	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const defaultArgoCDControllerNamespace = "argocd"

func defaultTrackingOptions() TrackingOptions {
	return TrackingOptions{
		Method:              string(argoappv1.TrackingMethodAnnotation),
		InstanceLabelKey:    common.LabelKeyAppInstance,
		ControllerNamespace: defaultArgoCDControllerNamespace,
	}
}

func trackingOptionsFromSettings(settings config.ArgoSettings) TrackingOptions {
	opts := defaultTrackingOptions()
	if settings.TrackingMethod.Value != "" {
		opts.Method = settings.TrackingMethod.Value
	}
	if settings.InstanceLabelKey.Value != "" {
		opts.InstanceLabelKey = settings.InstanceLabelKey.Value
	}
	opts.InstallationID = settings.InstallationID.Value
	return opts
}

func normalizeTrackingOptions(opts TrackingOptions) TrackingOptions {
	defaults := defaultTrackingOptions()
	if opts.Method == "" {
		opts.Method = defaults.Method
	}
	if opts.InstanceLabelKey == "" {
		opts.InstanceLabelKey = defaults.InstanceLabelKey
	}
	if opts.ControllerNamespace == "" {
		opts.ControllerNamespace = defaults.ControllerNamespace
	}
	return opts
}

func applyTrackingMetadata(application argoappv1.Application, obj *unstructured.Unstructured, opts TrackingOptions) error {
	if obj == nil {
		return nil
	}
	opts = normalizeTrackingOptions(opts)
	appInstanceName := application.InstanceName(opts.ControllerNamespace)
	if opts.InstanceLabelKey == "" || appInstanceName == "" || kube.IsCRD(obj) {
		return nil
	}
	if err := argoutil.NewResourceTracking().SetAppInstance(
		obj,
		opts.InstanceLabelKey,
		appInstanceName,
		application.Spec.Destination.Namespace,
		argoappv1.TrackingMethod(opts.Method),
		opts.InstallationID,
	); err != nil {
		return fmt.Errorf("failed to set app instance tracking info on manifest: %w", err)
	}
	return nil
}
