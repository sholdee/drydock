package render

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
)

// kustomizeHelmDefaultReleaseName is the release name of a helmCharts entry
// without releaseName. kustomize then runs helm template with
// --generate-name and no name (types.HelmChart.AsHelmArgs,
// api/types/helmchartargs.go:154-163 at kustomize/api v0.21.3), and helm
// template presets the name "release-name", which NameAndChart returns
// before it would generate one (pkg/cmd/template.go:104 and
// pkg/action/install.go:776-818 at helm v4.3.1).
const kustomizeHelmDefaultReleaseName = "release-name"

// helmDefaultNamespace is the release namespace helm uses when no
// --namespace is given and no kubeconfig names one. The Argo CD repo-server
// pod mounts no service account token and has no kubeconfig, so helm falls
// back to "default" (EnvSettings.Namespace, pkg/cli/environment.go:276-286
// at helm v4.3.1). Argo CD passes --namespace only when the namespace is
// not empty (util/helm/cmd.go:438-440 at v3.6.1), and kustomize does the
// same for helmCharts.
const helmDefaultNamespace = "default"

// helmReleaseNamespace returns .Release.Namespace for a render with opts.
func helmReleaseNamespace(opts RenderOptions) string {
	if opts.Namespace == "" {
		return helmDefaultNamespace
	}
	return opts.Namespace
}

// checkHelmCommandVersion fails as Argo CD v3.6.1 fails for a
// spec.source.helm.version that it does not support (NewCmd,
// util/helm/cmd.go:36-48, through NewHelmApp, util/helm/helm.go:47-51).
func checkHelmCommandVersion(version string) error {
	switch version {
	case "", "v3", "v4":
		return nil
	}
	return fmt.Errorf("failed to create new helm command: helm version '%s' is not supported", version)
}

// helmReleaseName returns the release name of the render. Argo CD passes
// the release name, or the Application name, to helm template as
// --name-template (util/helm/cmd.go:436 at v3.6.1), so helm renders it as a
// Go template first (action.TemplateName). kustomize passes the name of a
// helmCharts entry as an argument, which helm reads as written.
func helmReleaseName(opts RenderOptions) (string, error) {
	if opts.helmOutputForKustomize {
		if opts.ReleaseName == "" {
			return kustomizeHelmDefaultReleaseName, nil
		}
		return opts.ReleaseName, nil
	}
	name := opts.ReleaseName
	if name == "" {
		name = opts.AppName
	}
	return helmTemplateName(name)
}

// helmTemplateName is action.TemplateName (pkg/action/install.go:820-835 at
// helm v4.3.1). drydock does not import pkg/action, which brings in the
// Kubernetes client and the release storage drivers.
func helmTemplateName(nameTemplate string) (string, error) {
	if nameTemplate == "" {
		return "", nil
	}
	t, err := template.New("name-template").Funcs(sprig.TxtFuncMap()).Parse(nameTemplate)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, nil); err != nil {
		return "", err
	}
	return b.String(), nil
}

// loadInstallableHelmChart loads the chart and checks that helm template
// renders its type, as runInstall does right after it loads the chart.
func loadInstallableHelmChart(chartPath, manifestPath string, opts RenderOptions) (helmchart.Charter, error) {
	chrt, err := opts.HelmChartLoadCache.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load helm chart %s: %w", manifestPath, err)
	}
	if err := checkHelmChartInstallable(chrt); err != nil {
		return nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}
	return chrt, nil
}

// checkHelmChartInstallable is checkIfInstallable (pkg/cmd/install.go:360-371
// at helm v4.3.1), which helm template runs on the chart it loaded: only an
// application chart renders.
func checkHelmChartInstallable(chrt helmchart.Charter) error {
	accessor, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return err
	}
	switch accessor.MetadataAsMap()["Type"] {
	case "", "application":
		return nil
	}
	return fmt.Errorf("%s charts are not installable", accessor.MetadataAsMap()["Type"])
}

// checkHelmChartRelease runs the checks of Install.RunWithContext that come
// before the values are coalesced (pkg/action/install.go:296-322 at helm
// v4.3.1): helm template renders only a chart of apiVersion v1 or v2, and
// the release name must be valid (availableName,
// pkg/action/install.go:619-624).
func checkHelmChartRelease(chrt helmchart.Charter, releaseName string) (*chartv2.Chart, error) {
	var v2chart *chartv2.Chart
	switch typed := chrt.(type) {
	case *chartv2.Chart:
		v2chart = typed
	case chartv2.Chart:
		v2chart = &typed
	default:
		return nil, errors.New("invalid chart apiVersion")
	}
	if err := chartv2util.ValidateReleaseName(releaseName); err != nil {
		return nil, fmt.Errorf("release name check failed: release name %q: %w", releaseName, err)
	}
	return v2chart, nil
}

// checkHelmChartKubeVersion is the kubeVersion check of renderResources
// (pkg/action/action.go:288-291 at helm v4.3.1), which runs after the values
// are coalesced and checked against the schema, before the templates render.
func checkHelmChartKubeVersion(chrt *chartv2.Chart, capabilities *common.Capabilities) error {
	if chrt.Metadata == nil || chrt.Metadata.KubeVersion == "" {
		return nil
	}
	if !chartv2util.IsCompatibleRange(chrt.Metadata.KubeVersion, capabilities.KubeVersion.String()) {
		return fmt.Errorf("chart requires kubeVersion: %s which is incompatible with Kubernetes %s", chrt.Metadata.KubeVersion, capabilities.KubeVersion.Version)
	}
	return nil
}
