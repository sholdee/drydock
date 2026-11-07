package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// changedOnlyAppRevisionCode identifies a changed-only selection that the
// ARGOCD_APP_REVISION variables widened: the two sides render different
// commits, so every Application whose spec reads one of the variables
// renders differently whatever paths changed. It is strict-exempt
// (strictExemptDiagnostic): the selection is complete, not an ownership gap.
// No severity below warning exists.
const changedOnlyAppRevisionCode = "diff.changed-only-app-revision"

func changedOnlyAppRevisionDiagnostic(selected int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlyAppRevisionCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("the two sides render different commits; selecting %d Application(s) whose spec reads ARGOCD_APP_REVISION, ARGOCD_APP_REVISION_SHORT, or ARGOCD_APP_REVISION_SHORT_8, or that run a config management plugin command", selected),
	}
}

// diffSideTreeRevision returns the commit of the tree that a diff side
// renders: its snapshot revision, else the HEAD commit when its path is the
// top of a Git repository, else "".
func diffSideTreeRevision(request BuildRequest) string {
	if revision := strings.TrimSpace(request.rootRevision); revision != "" {
		return revision
	}
	return gitref.HeadRevision(request.Path)
}

// withAppRevisionSelection adds to the changed-only selection every
// Application that reads an ARGOCD_APP_REVISION variable, and every
// Application with a config management plugin source that this run renders
// with a command (pluginRunsWithBuildEnv), when the two sides render
// different commits. Its output changes with the commit, as in Argo CD
// (newEnv, reposerver/repository/repository.go:1864-1878), even when no
// path it owns changed. The test is the one that keys the persistent render
// cache (sourceReadsAppRevision), applied to the spec and to each local
// source merged with its .argocd-source overrides on that side. Applications
// they render are selected too. It returns how many Applications read a
// variable. injectedPlugins reports a PluginRenderer that renders every
// plugin source.
func withAppRevisionSelection(leftRequest, rightRequest BuildRequest, left, right selectionSide, leftSettings, rightSettings config.ArgoSettings, leftSelected, rightSelected []argoappv1.Application, injectedPlugins bool) ([]argoappv1.Application, []argoappv1.Application, int) {
	if diffSideTreeRevision(leftRequest) == diffSideTreeRevision(rightRequest) {
		return leftSelected, rightSelected, 0
	}
	keys := map[string]struct{}{}
	for _, side := range []struct {
		request  BuildRequest
		settings config.ArgoSettings
		inputs   []ApplicationSelectionInput
	}{
		{leftRequest, leftSettings, left.inputs},
		{rightRequest, rightSettings, right.inputs},
	} {
		controllerNamespace := trackingOptionsFromSettings(side.settings).ControllerNamespace
		if controllerNamespace == "" {
			controllerNamespace = defaultArgoCDControllerNamespace
		}
		for _, input := range side.inputs {
			pluginRuns := func(source argoappv1.ApplicationSource) bool {
				return pluginRunsWithBuildEnv(side.request, injectedPlugins, input.Application, source)
			}
			if applicationReadsAppRevision(side.request.Path, controllerNamespace, input.Application, pluginRuns) {
				keys[applicationKey(input.Application)] = struct{}{}
			}
		}
	}
	reached := len(keys)
	if reached == 0 {
		return leftSelected, rightSelected, 0
	}
	selectRenderedDescendants(keys, left.inputs, right.inputs)
	for _, application := range append(append([]argoappv1.Application(nil), leftSelected...), rightSelected...) {
		keys[applicationKey(application)] = struct{}{}
	}
	return selectedByKey(left.inputs, keys), selectedByKey(right.inputs, keys), reached
}

// applicationReadsAppRevision reports whether a source of application reads
// an ARGOCD_APP_REVISION variable, in its spec or after the .argocd-source
// overrides under root are merged into it, as PrepareSource merges them. A
// config management plugin source counts when pluginRuns reports that the
// run renders it with a command: the repo-server always puts the variables
// in the plugin environment (getPluginEnvs,
// reposerver/repository/repository.go:2398-2408), so that output can change
// with each commit. Plugin sources never persist, so this widening does not
// reach the render cache key (sourceReadsAppRevision).
func applicationReadsAppRevision(root, controllerNamespace string, application argoappv1.Application, pluginRuns func(argoappv1.ApplicationSource) bool) bool {
	sources := applicationRenderSources(application.Spec)
	reads := func(source argoappv1.ApplicationSource) bool {
		return sourceReadsAppRevision(source) || (source.Plugin != nil && pluginRuns != nil && pluginRuns(source))
	}
	for _, source := range sources {
		merged := source
		if strings.TrimSpace(source.Path) != "" {
			if clean, err := cleanLocalSourcePath(source.Path); err == nil {
				sourceDir := filepath.Join(root, clean)
				if info, err := os.Stat(sourceDir); err == nil && info.IsDir() {
					if overridden, err := mergeArgocdSourceOverrides(source, root, sourceDir, application.InstanceName(controllerNamespace)); err == nil {
						merged = overridden
					}
				}
			}
		}
		if reads(source) || reads(merged) {
			return true
		}
	}
	return false
}

// pluginRunsWithBuildEnv reports whether the render of source, a config
// management plugin source of application on the side that request
// renders, runs a command that gets the build environment. It asks the
// same questions as renderPluginSource, with the same source root: an
// injected PluginRenderer renders every plugin source. Otherwise only a
// trusted exec or container policy plugin with --enable-plugins runs a
// command, and only when every check before the command passes
// (prepareCommandPluginRender, which the renderer calls too). A native
// policy engine, the built-in argocd-vault-plugin compatibility and the
// native Kustomize CMP render in process without the build environment, and
// a plugin that drydock cannot render fails with plugin.unsupported, so
// none of them counts. A source that does not render from a local tree
// (the side root or a --repo-map directory) has the same revision on both
// sides, so it does not count either.
func pluginRunsWithBuildEnv(request BuildRequest, injectedPlugins bool, application argoappv1.Application, source argoappv1.ApplicationSource) bool {
	if source.Plugin == nil {
		return false
	}
	if injectedPlugins {
		return true
	}
	provider := localProvider{
		repoRoot:                request.Path,
		sourceResolver:          sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		pluginPolicy:            request.pluginPolicy,
		pluginPolicyExecTrusted: request.pluginPolicyExecTrusted,
	}
	if matcher := newSelfRepoMatcher(request.selfRepo); len(matcher.urlKeys) > 0 {
		provider.selfRepoURLKeys = matcher.urlKeys
		provider.selfRepoRevisions = matcher.revisions
	}
	resolved := render.ResolvedSource{
		Path:           source.Path,
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
	}
	// The render resolves the root as resolveSourceRootIdentity does:
	// a --repo-map target first, then the side root.
	if mapped, ok := provider.sourceResolver.MappedPath(source.RepoURL); ok {
		abs, err := filepath.Abs(mapped)
		if err != nil {
			return false
		}
		resolved.RepoRoot = abs
	} else if provider.sourceResolvesToRepoRoot(resolved) {
		resolved.RepoRoot = request.Path
	} else {
		return false
	}
	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		return false
	}
	opts.EnablePlugins = request.EnablePlugins
	plan, _, handled, err := provider.planPolicyPluginRender(resolved, opts)
	if !handled || err != nil {
		return false
	}
	switch plan.plugin.Engine {
	case pluginpolicy.EngineExec, pluginpolicy.EngineContainer:
		if !plan.opts.EnablePlugins || !provider.pluginPolicyExecTrusted {
			return false
		}
		_, _, err := prepareCommandPluginRender(resolved, plan.opts, plan.name, plan.plugin)
		return err == nil
	case pluginpolicy.EngineAVPCompat, pluginpolicy.EngineNativeKustomize:
		return false
	default:
		return false
	}
}
