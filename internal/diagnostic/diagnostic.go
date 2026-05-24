package diagnostic

import "strings"

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

const (
	CodePluginUnsupported = "plugin.unsupported"
	CodePluginFailed      = "plugin.failed"
)

type Provenance struct {
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
	Pointer string `json:"pointer,omitempty" yaml:"pointer,omitempty"`
}

type Diagnostic struct {
	Code       string     `json:"code,omitempty" yaml:"code,omitempty"`
	Severity   Severity   `json:"severity" yaml:"severity"`
	Category   string     `json:"category" yaml:"category"`
	Message    string     `json:"message" yaml:"message"`
	Provenance Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

type Reporter struct {
	strict     bool
	diagnostic []Diagnostic
}

func NewReporter(strict bool) *Reporter {
	return &Reporter{strict: strict}
}

func (r *Reporter) Warn(category, message string, provenance Provenance) {
	severity := SeverityWarning
	if r.strict {
		severity = SeverityError
	}
	diag := Diagnostic{
		Severity:   severity,
		Category:   category,
		Message:    message,
		Provenance: provenance,
	}
	diag.Code = StableCode(diag)
	r.diagnostic = append(r.diagnostic, diag)
}

func (r *Reporter) Error(category, message string, provenance Provenance) {
	diag := Diagnostic{
		Severity:   SeverityError,
		Category:   category,
		Message:    message,
		Provenance: provenance,
	}
	diag.Code = StableCode(diag)
	r.diagnostic = append(r.diagnostic, diag)
}

func (r *Reporter) All() []Diagnostic {
	out := make([]Diagnostic, len(r.diagnostic))
	copy(out, r.diagnostic)
	return out
}

func (r *Reporter) HasErrors() bool {
	for _, d := range r.diagnostic {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

func WithStableCodes(diags []Diagnostic) []Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	out := make([]Diagnostic, len(diags))
	copy(out, diags)
	for i := range out {
		if out[i].Code == "" {
			out[i].Code = StableCode(out[i])
		}
	}
	return out
}

func StableCode(diag Diagnostic) string {
	switch diag.Category {
	case "appset":
		return appSetCode(diag.Message)
	case "settings":
		return settingsCode(diag.Message)
	case "project":
		return projectCode(diag.Message)
	case "repository":
		return repositoryCode(diag.Message)
	case "plugin":
		return CodePluginUnsupported
	case "render":
		return "render.failed"
	case "repeated-resource":
		return "render.repeated-resource"
	case "changed-only":
		return "diff.changed-only-incomplete"
	}
	if diag.Category == "" {
		return "diagnostic.unspecified"
	}
	return diag.Category + ".unspecified"
}

func appSetCode(message string) string {
	switch {
	case strings.Contains(message, "provider fixture invalid"):
		return "appset.provider-fixture-invalid"
	case strings.Contains(message, "provider fixture supplied but no entries match"):
		return "appset.provider-no-match"
	case strings.Contains(message, "provider filter cannot be evaluated"):
		return "appset.provider-unsupported-filter"
	case strings.Contains(message, "unsupported ApplicationSet generator"):
		return "appset.unsupported-generator"
	default:
		return "appset.unspecified"
	}
}

func settingsCode(message string) string {
	switch {
	case strings.Contains(message, "conflicting"):
		return "settings.conflict"
	case strings.Contains(message, "failed to parse"):
		return "settings.parse-error"
	case strings.Contains(message, "parsed as metadata only") || strings.Contains(message, "parsed but not applied"):
		return "settings.metadata-only"
	default:
		return "settings.unspecified"
	}
}

func projectCode(message string) string {
	switch {
	case strings.Contains(message, "source repository") && strings.Contains(message, "not permitted"):
		return "project.source-repository-denied"
	case strings.Contains(message, "destination is not permitted"):
		return "project.destination-denied"
	case strings.Contains(message, "source namespace"):
		return "project.source-namespace-denied"
	case strings.Contains(message, "RBAC roles"):
		return "project.rbac-metadata-only"
	case strings.Contains(message, "permitOnlyProjectScopedClusters"):
		return "project.project-scoped-clusters-deferred"
	case strings.Contains(message, "references missing AppProject"):
		return "project.missing"
	default:
		return "project.unspecified"
	}
}

func repositoryCode(message string) string {
	switch {
	case strings.Contains(message, "missing repository metadata"):
		return "repository.metadata-missing"
	case strings.Contains(message, "repository metadata"):
		return "repository.project-mismatch"
	default:
		return "repository.unspecified"
	}
}
