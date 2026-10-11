package app

import (
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
)

// ociChartIgnoredCode identifies the advisory that spec.source.chart is
// ignored on an oci:// source. Argo CD's repo-server dispatches on
// source.IsOCI() before source.IsHelm() (reposerver/repository/repository.go:
// 358-364 at v3.5.4; IsOCI is a bare prefix check, types.go:337-339), so
// every oci:// repoURL is an OCI artifact source and chart never joins the
// pull, the routing, the override merge or the render. drydock mirrors that
// dispatch, so the manifests are the same whether or not the warning fires.
// The code is strict-exempt: normalizeDiagnostics and diagnosticFailure both
// skip it under --strict (see strictExemptDiagnostic), because Argo CD renders
// the shape silently and a strict failure would make drydock stricter than
// the system it mirrors.
const ociChartIgnoredCode = "source.oci-chart-ignored"

// ociChartIgnoredDiagnostic reports the advisory for an oci:// source that
// sets chart. The Argo-equivalent remedy comes first (drop chart, set
// path: . — a single-source spec needs path or chart and a multi-source
// entry needs path, chart or ref, util/argo/argo.go:588-604); the schemeless
// Helm-OCI alternative is offered only when RedactURL produced a safe
// spelling, so URL-carried credentials never reach the message.
func ociChartIgnoredDiagnostic(source render.ResolvedSource) (diagnostic.Diagnostic, bool) {
	chart := strings.TrimSpace(source.Chart)
	if chart == "" || !ociartifact.IsOCIURL(source.RepoURL) {
		return diagnostic.Diagnostic{}, false
	}
	redacted := ociartifact.RedactURL(source.RepoURL)
	rev := strings.TrimSpace(source.TargetRevision)
	message := fmt.Sprintf("chart %q is ignored: Argo CD treats every oci:// repository URL as an OCI artifact source and pulls %s at revision %q without reading spec.source.chart, so drydock renders that artifact; to silence this warning drop chart and set path: . (Argo CD requires path or chart on every source, or ref in a multi-source list)", chart, redacted, rev)
	// RedactURL fails closed to "[invalid-url]" when the spelling could still
	// carry credentials; the schemeless clause would repeat that spelling, so
	// the message ends after the path: . remedy instead.
	if redacted != "[invalid-url]" {
		schemeless := strings.TrimPrefix(redacted, "oci://")
		message += fmt.Sprintf("; only if you meant the chart named %q under repository %s, switch to the schemeless Helm OCI shape (repoURL %q, chart %q)", chart, schemeless, schemeless, chart)
	}
	// Always SeverityWarning. Direct construction (never Reporter.Warn) avoids
	// construction-time escalation under --strict, and StableCode has no
	// "source" case to derive the code from; the pipeline-wide escalation and
	// failure under --strict are skipped via strictExemptDiagnostic.
	return diagnostic.Diagnostic{
		Code:     ociChartIgnoredCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message:  message,
	}, true
}

// normalizeOCISource applies Argo CD's OCI dispatch to a resolved source:
// chart never participates, and an empty path selects the artifact root —
// apppathutil.Path(ociPath, source.Path) at repository.go:425 (v3.5.4), where
// util/app/path/path.go:17-36 returns the root for an empty path. Sources
// without the oci:// scheme are returned unchanged.
func normalizeOCISource(source render.ResolvedSource) render.ResolvedSource {
	if !ociartifact.IsOCIURL(source.RepoURL) {
		return source
	}
	source.Chart = ""
	if strings.TrimSpace(source.Path) == "" {
		source.Path = "."
	}
	return source
}

// prepareFailureDiagnostics returns the spec-only advisories RenderSource
// emits before acquiring sourcePlan. PrepareSource acquires every oci://
// source first (it merges .argocd-source*.yaml at the artifact path), so
// when that acquisition fails RenderSource never runs for the source; the
// oci-chart-ignored hint must still accompany the failure — the pull it
// explains is the one that failed, which is when the hint is most useful.
// Success and failure paths are exclusive, so the advisory is emitted once
// per (application, source) either way.
func prepareFailureDiagnostics(sourcePlan SourcePlan) []diagnostic.Diagnostic {
	if diag, ok := ociChartIgnoredDiagnostic(resolvedSourceForPlan(sourcePlan)); ok {
		return []diagnostic.Diagnostic{diag}
	}
	return nil
}
