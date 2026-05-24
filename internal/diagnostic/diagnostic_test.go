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

func TestReporterAssignsStableCodes(t *testing.T) {
	reporter := NewReporter(false)
	reporter.Warn("appset", "unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge", Provenance{})

	if got := reporter.All()[0].Code; got != "appset.unsupported-generator" {
		t.Fatalf("Code = %q, want appset.unsupported-generator", got)
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

func TestWithStableCodesPreservesExplicitCodes(t *testing.T) {
	diags := WithStableCodes([]Diagnostic{{
		Code:     "custom.explicit",
		Severity: SeverityWarning,
		Category: "custom",
		Message:  "message",
	}})
	if got := diags[0].Code; got != "custom.explicit" {
		t.Fatalf("Code = %q, want custom.explicit", got)
	}
}

func TestWithStableCodesAssignsKnownCodes(t *testing.T) {
	tests := []struct {
		name string
		diag Diagnostic
		want string
	}{
		{
			name: "unsupported appset generator",
			diag: Diagnostic{Severity: SeverityWarning, Category: "appset", Message: "unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge"},
			want: "appset.unsupported-generator",
		},
		{
			name: "project source denied",
			diag: Diagnostic{Severity: SeverityWarning, Category: "project", Message: "Application argocd/demo source repository \"https://github.com/example/repo\" is not permitted by AppProject \"platform\""},
			want: "project.source-repository-denied",
		},
		{
			name: "repository metadata missing",
			diag: Diagnostic{Severity: SeverityWarning, Category: "repository", Message: "Application argocd/demo source repository \"https://github.com/example/repo\" is missing repository metadata from discovered repository Secrets"},
			want: "repository.metadata-missing",
		},
		{
			name: "fallback",
			diag: Diagnostic{Severity: SeverityWarning, Category: "custom", Message: "message with variable value"},
			want: "custom.unspecified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithStableCodes([]Diagnostic{tt.diag})
			if got[0].Code != tt.want {
				t.Fatalf("Code = %q, want %q", got[0].Code, tt.want)
			}
		})
	}
}
