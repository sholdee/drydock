package app

import (
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
)

// sourceHydratorCode identifies the advisory for an Application with
// spec.sourceHydrator. The Argo CD v3.6.1 controller compares such an
// Application against its sync source only: the hydrated branch and path
// (ApplicationSpec.GetSources, pkg/apis/application/v1alpha1/types.go:278).
// drydock renders the same source, so its output matches the comparison
// whether or not the advisory fires. It cannot run the hydrator offline, so
// a change to the dry source shows only after the hydrator commits it to
// the sync branch. The code is strict-exempt (strictExemptDiagnostic): the
// render is the one Argo CD compares, so --strict has nothing to protect.
const sourceHydratorCode = "source.hydrator-sync-source"

// sourceHydratorDiagnostic reports the advisory for an Application with
// spec.sourceHydrator.
func sourceHydratorDiagnostic(application argoappv1.Application) (diagnostic.Diagnostic, bool) {
	hydrator := application.Spec.SourceHydrator
	if hydrator == nil {
		return diagnostic.Diagnostic{}, false
	}
	syncSource := hydrator.GetSyncSource()
	return diagnostic.Diagnostic{
		Code:     sourceHydratorCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message: fmt.Sprintf("Application %s uses spec.sourceHydrator: drydock renders the sync source %s at branch %q, path %q, as Argo CD compares it, and ignores spec.source and spec.sources; drydock does not run the hydrator, so changes to the dry source %q show only after the hydrator commits them",
			applicationDisplayName(application), remote.RedactGitRepoURL(syncSource.RepoURL), syncSource.TargetRevision, syncSource.Path, hydrator.DrySource.Path),
		Provenance: diagnostic.Provenance{Path: applicationDisplayName(application)},
	}, true
}
