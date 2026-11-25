package render

import (
	"context"
	"errors"
	"fmt"

	argohelm "github.com/argoproj/argo-cd/v3/util/helm"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
)

// helmTemplateDefaultReleaseName is the release name of a helm template
// run that gets no name. helm template presets the name "release-name",
// which NameAndChart returns before it would generate one
// (pkg/cmd/template.go:104 and pkg/action/install.go:776-818 at helm
// v4.3.2). kustomize runs helm template with --generate-name and no name for
// a helmCharts entry without releaseName (types.HelmChart.AsHelmArgs,
// api/types/helmchartargs.go:154-163 at kustomize/api v0.21.3). Argo CD
// v3.6.3 passes no name when the release name and the Application name are
// both empty (Cmd.template, util/helm/cmd.go:475-492).
const helmTemplateDefaultReleaseName = "release-name"

// helmDefaultNamespace is the release namespace helm uses when no
// --namespace is given and no kubeconfig names one. The Argo CD repo-server
// pod mounts no service account token and has no kubeconfig, so helm falls
// back to "default" (EnvSettings.Namespace, pkg/cli/environment.go:276-286
// at helm v4.3.1). Argo CD passes --namespace only when the namespace is
// not empty (util/helm/cmd.go:438-440 at v3.6.1), and kustomize does the
// same for helmCharts.
const helmDefaultNamespace = "default"

// helmRenderStartError returns the error that stops a Helm render before it
// reads anything: a canceled context, or an error in the helm block of the
// source (HelmSpecError).
func helmRenderStartError(ctx context.Context, opts RenderOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return opts.HelmSpecError
}

// helmReleaseNamespace returns .Release.Namespace for a render with opts.
// Without a namespace, helm uses the namespace of its environment in the
// repo-server (HelmDefaultNamespace, see config.RepoServerHelmNamespace),
// which is "default" without a service account token.
func helmReleaseNamespace(opts RenderOptions) string {
	if opts.Namespace != "" {
		return opts.Namespace
	}
	if opts.HelmDefaultNamespace != "" {
		return opts.HelmDefaultNamespace
	}
	return helmDefaultNamespace
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

// checkHelmSourceReleaseName is the check that Argo CD v3.6.3 runs on
// helm.releaseName in helmTemplate (reposerver/repository/repository.go:
// 1321-1327), before it resolves the value files. It does not apply to a
// helmCharts entry, which kustomize passes to helm.
func checkHelmSourceReleaseName(opts RenderOptions) error {
	if opts.helmOutputForKustomize || opts.ReleaseName == "" {
		return nil
	}
	if err := argohelm.ValidateReleaseName(opts.ReleaseName); err != nil {
		return fmt.Errorf("invalid spec.source.helm.releaseName: %w", err)
	}
	return nil
}

// helmReleaseName returns the release name of the render. Argo CD v3.6.3
// passes helm.releaseName, or else the Application name, to helm template
// as the NAME argument, and it checks the name before it runs helm
// (Cmd.template, util/helm/cmd.go:475-492). helm reads the argument as
// written. Argo CD v3.6.2 and earlier passed it as --name-template, which
// helm evaluates as a Go template with the sprig functions
// (GHSA-v2hj-322q-6cg4). kustomize passes the name of a helmCharts entry as
// an argument too, without the Argo CD check.
func helmReleaseName(opts RenderOptions) (string, error) {
	if opts.helmOutputForKustomize {
		if opts.ReleaseName == "" {
			return helmTemplateDefaultReleaseName, nil
		}
		return opts.ReleaseName, nil
	}
	name := opts.ReleaseName
	if name == "" {
		name = opts.AppName
	}
	if name == "" {
		return helmTemplateDefaultReleaseName, nil
	}
	if err := argohelm.ValidateReleaseName(name); err != nil {
		return "", fmt.Errorf("failed to execute helm template command: %w", err)
	}
	return name, nil
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
// pkg/action/install.go:619-624). For a Helm source, the Argo CD check in
// helmReleaseName already rejected every name that this check rejects, so
// the release name check fails only for a helmCharts entry.
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
//
// Argo CD checks the kube version of the destination cluster. drydock knows
// that version only when it is explicit: a per-app kubeVersion,
// --kube-version, or a helmCharts kubeVersion. Without one, drydock renders
// with helm's default capabilities, so the check is skipped rather than
// failing a chart that the cluster would accept.
func checkHelmChartKubeVersion(chrt *chartv2.Chart, capabilities *common.Capabilities, explicitKubeVersion string) error {
	if explicitKubeVersion == "" || chrt.Metadata == nil || chrt.Metadata.KubeVersion == "" {
		return nil
	}
	if !chartv2util.IsCompatibleRange(chrt.Metadata.KubeVersion, capabilities.KubeVersion.String()) {
		return fmt.Errorf("chart requires kubeVersion: %s which is incompatible with Kubernetes %s", chrt.Metadata.KubeVersion, capabilities.KubeVersion.Version)
	}
	return nil
}
