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
		Message:  fmt.Sprintf("the two sides render different commits; selecting %d Application(s) whose spec reads ARGOCD_APP_REVISION, ARGOCD_APP_REVISION_SHORT, or ARGOCD_APP_REVISION_SHORT_8", selected),
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
// Application that reads an ARGOCD_APP_REVISION variable when the two sides
// render different commits. Its output changes with the commit, as in Argo
// CD (newEnv, reposerver/repository/repository.go:1864-1878), even when no
// path it owns changed. The test is the one that keys the persistent render
// cache (sourceReadsAppRevision), applied to the spec and to each local
// source merged with its .argocd-source overrides on that side. Applications
// they render are selected too. It returns how many Applications read a
// variable.
func withAppRevisionSelection(leftRequest, rightRequest BuildRequest, left, right selectionSide, leftSettings, rightSettings config.ArgoSettings, leftSelected, rightSelected []argoappv1.Application) ([]argoappv1.Application, []argoappv1.Application, int) {
	if diffSideTreeRevision(leftRequest) == diffSideTreeRevision(rightRequest) {
		return leftSelected, rightSelected, 0
	}
	keys := map[string]struct{}{}
	for _, side := range []struct {
		root     string
		settings config.ArgoSettings
		inputs   []ApplicationSelectionInput
	}{
		{leftRequest.Path, leftSettings, left.inputs},
		{rightRequest.Path, rightSettings, right.inputs},
	} {
		controllerNamespace := trackingOptionsFromSettings(side.settings).ControllerNamespace
		if controllerNamespace == "" {
			controllerNamespace = defaultArgoCDControllerNamespace
		}
		for _, input := range side.inputs {
			if applicationReadsAppRevision(side.root, controllerNamespace, input.Application) {
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
// overrides under root are merged into it, as PrepareSource merges them.
func applicationReadsAppRevision(root, controllerNamespace string, application argoappv1.Application) bool {
	sources := application.Spec.Sources
	if len(sources) == 0 && application.Spec.Source != nil {
		sources = argoappv1.ApplicationSources{*application.Spec.Source}
	}
	for _, source := range sources {
		if sourceReadsAppRevision(source) {
			return true
		}
		if strings.TrimSpace(source.Path) == "" {
			continue
		}
		clean, err := cleanLocalSourcePath(source.Path)
		if err != nil {
			continue
		}
		sourceDir := filepath.Join(root, clean)
		if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
			continue
		}
		merged, err := mergeArgocdSourceOverrides(source, sourceDir, application.InstanceName(controllerNamespace))
		if err == nil && sourceReadsAppRevision(merged) {
			return true
		}
	}
	return false
}
