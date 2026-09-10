package app

import (
	"fmt"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

// policyPluginBuildEnvEntries returns the eleven build-environment entries
// (opts.ArgoEnv then KUBE_VERSION and KUBE_API_VERSIONS) as an Env, the exact
// substitution scope a repo-server applies to spec.source.plugin.env values
// (reposerver/repository/repository.go getPluginParamEnvs): host env.allow
// values and earlier ARGOCD_ENV_* entries are deliberately not substitutable.
func policyPluginBuildEnvEntries(opts render.RenderOptions) argoappv1.Env {
	// Envsubst dereferences every entry, so nil entries (legal in the public
	// RenderOptions.ArgoEnv) are dropped rather than passed through.
	entries := make(argoappv1.Env, 0, len(opts.ArgoEnv)+2)
	for _, entry := range opts.ArgoEnv {
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	entries = append(entries,
		&argoappv1.EnvEntry{Name: "KUBE_VERSION", Value: opts.KubeVersion},
		&argoappv1.EnvEntry{Name: "KUBE_API_VERSIONS", Value: strings.Join(opts.APIVersions, ",")},
	)
	return entries
}

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
	return policyPluginBuildEnvEntries(opts).Environ()
}

// composePolicyPluginExtraEnv orders everything drydock itself provides to a
// command-backed plugin, in the repo-server's order: the build environment,
// the Application env delivered as ARGOCD_ENV_*, the Application parameter
// environment (ARGOCD_APP_PARAMETERS then PARAM_*), then drydock extras.
// pluginexec.BuildEnv places these after the policy env.allow copies.
func composePolicyPluginExtraEnv(opts render.RenderOptions, applicationEnv []string, params validatedPluginParameters, offline bool) []string {
	env := policyPluginBuildEnv(opts)
	env = append(env, applicationEnv...)
	env = append(env, params.extraEnv...)
	if offline {
		env = append(env, "DRYDOCK_OFFLINE=true")
	}
	return env
}

// maxApplicationPluginEnvValueBytes is the runner's env value cap. Checking it
// here, before pluginexec.BuildEnv does, yields a value-free diagnostic that
// names the Application env entry instead of the runner's generic error.
const maxApplicationPluginEnvValueBytes = pluginexec.MaxEnvValueBytes

// validateApplicationPluginEnv checks spec.source.plugin.env against the
// policy's applicationEnv.allow and drydock's env rules, then returns the
// ARGOCD_ENV_<name>=<value> entries in spec order with each value expanded
// against buildEnv (Argo CD's Envsubst: $VAR/${VAR}, $$ -> $, unknown ->
// empty). It fails closed with a message that names every entry missing from
// the allowlist but never a value: Application env may carry secrets. Values
// are not added to the redaction set because a repo-server treats them as
// non-secret too.
func validateApplicationPluginEnv(name string, allow []string, env argoappv1.Env, buildEnv argoappv1.Env) ([]string, string) {
	if len(env) == 0 {
		return nil, ""
	}
	allowed := make(map[string]struct{}, len(allow))
	for _, entry := range allow {
		allowed[entry] = struct{}{}
	}
	seen := make(map[string]struct{}, len(env))
	var notAllowed []string
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if entry == nil || strings.TrimSpace(entry.Name) == "" {
			return nil, fmt.Sprintf("config management plugin %s has an unnamed Application plugin env entry", pluginDisplayName(name))
		}
		if !pluginpolicy.IsValidEnvName(entry.Name) {
			return nil, fmt.Sprintf("config management plugin %s has invalid Application plugin env name %q", pluginDisplayName(name), entry.Name)
		}
		if _, ok := seen[entry.Name]; ok {
			return nil, fmt.Sprintf("config management plugin %s has duplicate Application plugin env %q", pluginDisplayName(name), entry.Name)
		}
		seen[entry.Name] = struct{}{}
		if _, ok := allowed[entry.Name]; !ok {
			// Keep going: one message that names every missing entry saves a
			// policy edit per name.
			notAllowed = append(notAllowed, entry.Name)
			continue
		}
		if len(entry.Value) > maxApplicationPluginEnvValueBytes {
			return nil, fmt.Sprintf("config management plugin %s Application plugin env %q value is too large", pluginDisplayName(name), entry.Name)
		}
		if strings.ContainsRune(entry.Value, 0) {
			return nil, fmt.Sprintf("config management plugin %s Application plugin env %q value contains a NUL byte", pluginDisplayName(name), entry.Name)
		}
		expanded := buildEnv.Envsubst(entry.Value)
		if len(expanded) > maxApplicationPluginEnvValueBytes {
			return nil, fmt.Sprintf("config management plugin %s Application plugin env %q value is too large after build-environment substitution", pluginDisplayName(name), entry.Name)
		}
		out = append(out, pluginpolicy.ApplicationEnvPrefix+entry.Name+"="+expanded)
	}
	if len(notAllowed) == 1 {
		return nil, fmt.Sprintf("config management plugin %s uses Application plugin env %q, which is not allowed by policy applicationEnv.allow", pluginDisplayName(name), notAllowed[0])
	}
	if len(notAllowed) > 1 {
		return nil, fmt.Sprintf("config management plugin %s uses Application plugin env %s, which are not allowed by policy applicationEnv.allow", pluginDisplayName(name), quotedEnvNames(notAllowed))
	}
	return out, ""
}

// quotedEnvNames renders names as `"A", "B"` so the plural fail-closed message
// reads like the single-entry one.
func quotedEnvNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, strconv.Quote(name))
	}
	return strings.Join(quoted, ", ")
}
