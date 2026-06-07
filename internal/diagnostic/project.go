package diagnostic

import (
	"fmt"
	"strings"
)

const (
	CodeProjectMissing                        = "project.missing"
	CodeProjectSourceRepositoryDenied         = "project.source-repository-denied"
	CodeProjectDestinationDenied              = "project.destination-denied"
	CodeProjectSourceNamespaceDenied          = "project.source-namespace-denied"
	CodeProjectResourceDenied                 = "project.resource-denied"
	CodeProjectResourceDestinationDenied      = "project.resource-destination-denied"
	CodeProjectResourceScopeDeferred          = "project.resource-scope-deferred"
	CodeProjectScopedClustersDeferred         = "project.project-scoped-clusters-deferred"
	CodeProjectRBACMetadataOnly               = "project.rbac-metadata-only"
	CodeProjectUnspecified                    = "project.unspecified"
	CodeRepositoryMetadataMissing             = "repository.metadata-missing"
	CodeRepositoryProjectMismatch             = "repository.project-mismatch"
	CodeClusterMetadataMissing                = "cluster.metadata-missing"
	CodeClusterProjectMismatch                = "cluster.project-mismatch"
	projectDiagnosticsModeSupportedValuesText = "actionable, all, or off"
)

type ProjectDiagnosticsMode string

const (
	ProjectDiagnosticsModeActionable ProjectDiagnosticsMode = "actionable"
	ProjectDiagnosticsModeAll        ProjectDiagnosticsMode = "all"
	ProjectDiagnosticsModeOff        ProjectDiagnosticsMode = "off"
)

func ParseProjectDiagnosticsMode(value string) (ProjectDiagnosticsMode, error) {
	mode := ProjectDiagnosticsMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		return ProjectDiagnosticsModeActionable, nil
	}
	if err := mode.Validate(); err != nil {
		return "", fmt.Errorf("project diagnostics mode must be %s, got %q", projectDiagnosticsModeSupportedValuesText, value)
	}
	return mode, nil
}

func (mode ProjectDiagnosticsMode) Normalize() ProjectDiagnosticsMode {
	if mode == "" {
		return ProjectDiagnosticsModeActionable
	}
	return mode
}

func (mode ProjectDiagnosticsMode) Validate() error {
	switch mode.Normalize() {
	case ProjectDiagnosticsModeActionable, ProjectDiagnosticsModeAll, ProjectDiagnosticsModeOff:
		return nil
	default:
		return fmt.Errorf("project diagnostics mode must be %s, got %q", projectDiagnosticsModeSupportedValuesText, string(mode))
	}
}

type ProjectDiagnosticClass string

const (
	ProjectDiagnosticClassNonProject   ProjectDiagnosticClass = "non-project"
	ProjectDiagnosticClassActionable   ProjectDiagnosticClass = "actionable"
	ProjectDiagnosticClassDeferred     ProjectDiagnosticClass = "deferred"
	ProjectDiagnosticClassMetadataOnly ProjectDiagnosticClass = "metadata-only"
	ProjectDiagnosticClassOther        ProjectDiagnosticClass = "other"
)

func ClassifyProjectDiagnostic(diag Diagnostic) ProjectDiagnosticClass {
	code := classificationCode(diag)
	switch code {
	case CodeProjectMissing,
		CodeProjectSourceRepositoryDenied,
		CodeProjectDestinationDenied,
		CodeProjectSourceNamespaceDenied,
		CodeProjectResourceDenied,
		CodeProjectResourceDestinationDenied:
		return ProjectDiagnosticClassActionable
	case CodeProjectResourceScopeDeferred,
		CodeProjectScopedClustersDeferred:
		return ProjectDiagnosticClassDeferred
	case CodeProjectRBACMetadataOnly,
		CodeRepositoryMetadataMissing,
		CodeRepositoryProjectMismatch,
		CodeClusterMetadataMissing,
		CodeClusterProjectMismatch:
		return ProjectDiagnosticClassMetadataOnly
	case CodeProjectUnspecified:
		if projectUnspecifiedDeferredMessage(diag.Message) {
			return ProjectDiagnosticClassDeferred
		}
		return ProjectDiagnosticClassOther
	}
	if strings.HasPrefix(code, "project.") || diag.Category == "project" {
		return ProjectDiagnosticClassOther
	}
	return ProjectDiagnosticClassNonProject
}

func FilterProjectDiagnostics(diags []Diagnostic, mode ProjectDiagnosticsMode) []Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	mode = mode.Normalize()
	out := make([]Diagnostic, 0, len(diags))
	for _, diag := range diags {
		class := ClassifyProjectDiagnostic(diag)
		switch mode {
		case ProjectDiagnosticsModeAll:
			out = append(out, diag)
		case ProjectDiagnosticsModeOff:
			if class == ProjectDiagnosticClassNonProject {
				out = append(out, diag)
			}
		case ProjectDiagnosticsModeActionable:
			if class == ProjectDiagnosticClassActionable || class == ProjectDiagnosticClassNonProject {
				out = append(out, diag)
			}
		default:
			if class == ProjectDiagnosticClassActionable || class == ProjectDiagnosticClassNonProject {
				out = append(out, diag)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func classificationCode(diag Diagnostic) string {
	if code := strings.TrimSpace(diag.Code); code != "" {
		return code
	}
	return StableCode(diag)
}

func projectUnspecifiedDeferredMessage(message string) bool {
	return strings.Contains(message, "could not be validated against AppProject") ||
		(strings.Contains(message, "destination name") &&
			strings.Contains(message, "cannot be resolved against AppProject server policy offline"))
}
