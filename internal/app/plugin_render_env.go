package app

import (
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
)

// policyPluginBuildEnv returns the Argo CD build environment a repo-server
// hands a config management plugin, in the repo-server's order: the nine
// ARGOCD_APP_* entries, then KUBE_VERSION and KUBE_API_VERSIONS
// (reposerver/repository/repository.go getPluginEnvs). Both KUBE_* keys are
// always present and empty when drydock has no --kube-version or
// --api-versions input, matching Argo CD's key presence so plugins that test
// for the variable behave the same way. opts.KubeVersion and opts.APIVersions
// here can only be the --kube-version/--api-versions capability values (a
// plugin source cannot carry spec.helm/kustomize.kubeVersion: multi-type
// sources are rejected at plan time), which matches the repo-server sending
// the cluster version to plugins. KUBE_API_VERSIONS is sorted and
// deduplicated, whereas a repo-server sends discovery order.
func policyPluginBuildEnv(opts render.RenderOptions) []string {
	env := append([]string(nil), opts.ArgoEnv.Environ()...)
	env = append(env, "KUBE_VERSION="+opts.KubeVersion)
	return append(env, "KUBE_API_VERSIONS="+strings.Join(opts.APIVersions, ","))
}

// composePolicyPluginExtraEnv orders everything drydock itself provides to a
// command-backed plugin: the build environment, the Application parameter
// environment (ARGOCD_APP_PARAMETERS then PARAM_*), then drydock extras.
// pluginexec.BuildEnv places these after the policy env.allow copies.
func composePolicyPluginExtraEnv(opts render.RenderOptions, params validatedPluginParameters, offline bool) []string {
	env := policyPluginBuildEnv(opts)
	env = append(env, params.extraEnv...)
	if offline {
		env = append(env, "DRYDOCK_OFFLINE=true")
	}
	return env
}

// applicationPluginEnvNames lists the names in spec.source.plugin.env for
// diagnostics. Values are never included: Application env may carry secrets.
func applicationPluginEnvNames(env argoappv1.Env) []string {
	names := make([]string, 0, len(env))
	for _, entry := range env {
		if entry == nil || strings.TrimSpace(entry.Name) == "" {
			continue
		}
		names = append(names, entry.Name)
	}
	return names
}
