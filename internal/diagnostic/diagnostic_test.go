package diagnostic

import "testing"

func TestReporterWarnAndStrict(t *testing.T) {
	reporter := NewReporter(false)
	reporter.Warn("settings", "missing argocd-cm", Provenance{Path: "apps/argocd/values.yaml", Pointer: "configs.cm"})

	if reporter.HasErrors() {
		t.Fatalf("non-strict warning should not be an error")
	}
	if got := reporter.All(); len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("unexpected diagnostics: %#v", got)
	}

	strict := NewReporter(true)
	strict.Warn("settings", "conflicting argocd-cm", Provenance{Path: "a.yaml", Pointer: "data"})
	if !strict.HasErrors() {
		t.Fatalf("strict warning should be promoted to error")
	}
	if got := strict.All()[0]; got.Severity != SeverityError {
		t.Fatalf("strict severity = %s, want error", got.Severity)
	}
}

func TestReporterError(t *testing.T) {
	reporter := NewReporter(false)
	reporter.Error("render", "failed to parse manifest", Provenance{Path: "bad.yaml"})

	if !reporter.HasErrors() {
		t.Fatalf("error should set HasErrors")
	}
}

func TestReporterAllReturnsCopy(t *testing.T) {
	reporter := NewReporter(false)
	reporter.Warn("settings", "missing argocd-cm", Provenance{Path: "values.yaml"})

	got := reporter.All()
	got[0].Message = "mutated"
	got = append(got, Diagnostic{Severity: SeverityError})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}

	again := reporter.All()
	if len(again) != 1 {
		t.Fatalf("len(All()) = %d, want 1", len(again))
	}
	if again[0].Message != "missing argocd-cm" {
		t.Fatalf("stored diagnostic was mutated: %#v", again[0])
	}
}
