package app

import (
	"fmt"
	"regexp"

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/utils/kube"
	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// defaultArgoCDControllerNamespace is the Argo CD controller namespace that
// drydock assumes when nothing in the repository names one.
const defaultArgoCDControllerNamespace = config.DefaultControllerNamespace

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
	// The application controller builds the instance name, which
	// ARGOCD_APP_NAME, the .argocd-source-<app>.yaml lookup, and resource
	// tracking read, with its own namespace (controller/state.go:402 and
	// :797 at v3.6.1). config.ArgoSettings.ControllerNamespace is the rule
	// that the namespace gate and AppProject validation use too.
	opts.ControllerNamespace = settings.ControllerNamespace("")
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
		return fmt.Errorf("failed to set app instance tracking info on manifest: %s", sanitizeTrackingError(err))
	}
	return nil
}

// trackingAccessorValue matches the field value that an apimachinery
// unstructured accessor error prints, such as the "text" in
// ".metadata.annotations accessor error: text is of the type string,
// expected map[string]interface{}" (unstructured/helpers.go at v0.37.0). The
// %T type name that follows never holds " is of the type ", so the greedy
// match ends at the type name.
var trackingAccessorValue = regexp.MustCompile(`(?s)(accessor error: (?:contains non-string (?:key in the slice|value in the map under key "(?:[^"\\]|\\.)*"): )?).* is of the type `)

// sanitizeTrackingError keeps the text of a SetAppInstance error, which
// Argo CD prints as is, but drops the field value that the accessor error
// copies, because an annotation or label value can hold a secret.
func sanitizeTrackingError(err error) string {
	return trackingAccessorValue.ReplaceAllString(err.Error(), "${1}a value is of the type ")
}
