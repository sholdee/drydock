package diagnostic

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Provenance struct {
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
	Pointer string `json:"pointer,omitempty" yaml:"pointer,omitempty"`
}

type Diagnostic struct {
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
	r.diagnostic = append(r.diagnostic, Diagnostic{
		Severity:   severity,
		Category:   category,
		Message:    message,
		Provenance: provenance,
	})
}

func (r *Reporter) Error(category, message string, provenance Provenance) {
	r.diagnostic = append(r.diagnostic, Diagnostic{
		Severity:   SeverityError,
		Category:   category,
		Message:    message,
		Provenance: provenance,
	})
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
