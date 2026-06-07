package diagnostic

import (
	"strings"
	"testing"
)

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
			name: "project rendered resource denied",
			diag: Diagnostic{Severity: SeverityWarning, Category: "project", Message: "Application argocd/demo rendered resource ConfigMap workloads/settings is not permitted by AppProject \"platform\""},
			want: "project.resource-denied",
		},
		{
			name: "project rendered resource destination denied",
			diag: Diagnostic{Severity: SeverityWarning, Category: "project", Message: "Application argocd/demo rendered resource ConfigMap kube-system/settings namespace \"kube-system\" is not permitted by AppProject \"platform\""},
			want: "project.resource-destination-denied",
		},
		{
			name: "project rendered resource scope deferred",
			diag: Diagnostic{Severity: SeverityWarning, Category: "project", Message: "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred"},
			want: "project.resource-scope-deferred",
		},
		{
			name: "repository metadata missing",
			diag: Diagnostic{Severity: SeverityWarning, Category: "repository", Message: "Application argocd/demo source repository \"https://github.com/example/repo\" is missing repository metadata from discovered repository Secrets"},
			want: CodeRepositoryMetadataMissing,
		},
		{
			name: "cluster metadata project mismatch",
			diag: Diagnostic{Severity: SeverityWarning, Category: "cluster", Message: "Application argocd/demo cluster metadata for \"in-cluster\" is scoped to project \"infra\", not AppProject \"platform\""},
			want: CodeClusterProjectMismatch,
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

func TestParseProjectDiagnosticsMode(t *testing.T) {
	tests := []struct {
		value string
		want  ProjectDiagnosticsMode
	}{
		{value: "", want: ProjectDiagnosticsModeActionable},
		{value: "actionable", want: ProjectDiagnosticsModeActionable},
		{value: " ACTIONABLE ", want: ProjectDiagnosticsModeActionable},
		{value: "all", want: ProjectDiagnosticsModeAll},
		{value: "OFF", want: ProjectDiagnosticsModeOff},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseProjectDiagnosticsMode(tt.value)
			if err != nil {
				t.Fatalf("ParseProjectDiagnosticsMode(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseProjectDiagnosticsMode(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}

	var zero ProjectDiagnosticsMode
	if got := zero.Normalize(); got != ProjectDiagnosticsModeActionable {
		t.Fatalf("zero mode Normalize() = %q, want actionable", got)
	}
	if err := zero.Validate(); err != nil {
		t.Fatalf("zero mode Validate() error = %v", err)
	}
}

func TestParseProjectDiagnosticsModeRejectsInvalid(t *testing.T) {
	_, err := ParseProjectDiagnosticsMode("verbose")
	if err == nil {
		t.Fatal("ParseProjectDiagnosticsMode() error = nil, want invalid mode error")
	}
	if !strings.Contains(err.Error(), `project diagnostics mode must be actionable, all, or off, got "verbose"`) {
		t.Fatalf("ParseProjectDiagnosticsMode() error = %v, want invalid mode message", err)
	}

	if err := ProjectDiagnosticsMode("verbose").Validate(); err == nil {
		t.Fatal("ProjectDiagnosticsMode.Validate() error = nil, want invalid mode error")
	}
}

func TestFilterProjectDiagnosticsActionableKeepsLocalDenials(t *testing.T) {
	diags := []Diagnostic{
		projectDiagnostic(CodeProjectMissing, "Application argocd/demo references missing AppProject \"platform\""),
		projectDiagnostic(CodeProjectSourceRepositoryDenied, "Application argocd/demo source repository \"https://github.com/example/repo\" is not permitted by AppProject \"platform\""),
		projectDiagnostic(CodeProjectDestinationDenied, "Application argocd/demo destination is not permitted by AppProject \"platform\""),
		projectDiagnostic(CodeProjectSourceNamespaceDenied, "Application argocd/demo source namespace \"team-a\" is not permitted by AppProject \"platform\""),
		projectDiagnostic(CodeProjectResourceDenied, "Application argocd/demo rendered resource ConfigMap workloads/settings is not permitted by AppProject \"platform\""),
		projectDiagnostic(CodeProjectResourceDestinationDenied, "Application argocd/demo rendered resource ConfigMap kube-system/settings namespace \"kube-system\" is not permitted by AppProject \"platform\""),
		renderDiagnostic("Application argocd/demo failed to render: boom"),
	}

	got := FilterProjectDiagnostics(diags, "")
	if len(got) != len(diags) {
		t.Fatalf("FilterProjectDiagnostics(actionable) len = %d, want %d: %#v", len(got), len(diags), got)
	}
	for _, diag := range got {
		if ClassifyProjectDiagnostic(diag) == ProjectDiagnosticClassDeferred || ClassifyProjectDiagnostic(diag) == ProjectDiagnosticClassMetadataOnly {
			t.Fatalf("FilterProjectDiagnostics(actionable) kept hidden diagnostic: %#v", diag)
		}
	}
}

func TestFilterProjectDiagnosticsActionableUsesStableCodeFallback(t *testing.T) {
	diags := []Diagnostic{
		{
			Severity: SeverityWarning,
			Category: "project",
			Message:  "Application argocd/demo source repository \"https://github.com/example/repo\" is not permitted by AppProject \"platform\"",
		},
		{
			Severity: SeverityWarning,
			Category: "project",
			Message:  "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred",
		},
	}

	got := FilterProjectDiagnostics(diags, ProjectDiagnosticsModeActionable)
	if len(got) != 1 {
		t.Fatalf("FilterProjectDiagnostics(actionable) len = %d, want 1: %#v", len(got), got)
	}
	if class := ClassifyProjectDiagnostic(diags[0]); class != ProjectDiagnosticClassActionable {
		t.Fatalf("ClassifyProjectDiagnostic(source denial) = %q, want actionable", class)
	}
	if got[0].Message != diags[0].Message {
		t.Fatalf("FilterProjectDiagnostics(actionable)[0] = %#v, want source denial", got[0])
	}
}

func TestFilterProjectDiagnosticsActionableHidesDeferredAndMetadataOnly(t *testing.T) {
	diags := []Diagnostic{
		projectDiagnostic(CodeProjectResourceScopeDeferred, "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred"),
		projectDiagnostic(CodeProjectScopedClustersDeferred, "AppProject \"platform\" enables permitOnlyProjectScopedClusters; project-scoped cluster Secrets enforcement is deferred offline"),
		projectDiagnostic(CodeProjectRBACMetadataOnly, "AppProject \"platform\" defines RBAC roles; offline validation reports role presence but does not simulate authorization"),
		projectDiagnostic(CodeProjectUnspecified, "Application argocd/demo destination could not be validated against AppProject \"platform\": cluster metadata is unavailable"),
		projectDiagnostic(CodeProjectUnspecified, "Application argocd/demo destination name \"in-cluster\" cannot be resolved against AppProject server policy offline"),
		{
			Severity: SeverityWarning,
			Category: "repository",
			Message:  "Application argocd/demo repository metadata for \"https://github.com/example/repo\" is scoped to project \"infra\", not AppProject \"platform\"",
		},
		{
			Code:     CodeRepositoryMetadataMissing,
			Severity: SeverityWarning,
			Category: "repository",
			Message:  "Application argocd/demo source repository \"https://github.com/example/repo\" is missing repository metadata from discovered repository Secrets",
		},
		{
			Code:     CodeClusterProjectMismatch,
			Severity: SeverityWarning,
			Category: "cluster",
			Message:  "Application argocd/demo cluster metadata for \"in-cluster\" is scoped to project \"infra\", not AppProject \"platform\"",
		},
		renderDiagnostic("Application argocd/demo failed to render: boom"),
	}

	got := FilterProjectDiagnostics(diags, ProjectDiagnosticsModeActionable)
	if len(got) != 1 {
		t.Fatalf("FilterProjectDiagnostics(actionable) len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Category != "render" {
		t.Fatalf("FilterProjectDiagnostics(actionable)[0] = %#v, want render diagnostic", got[0])
	}
}

func TestFilterProjectDiagnosticsActionableHidesUnknownProjectAdjacentDiagnostics(t *testing.T) {
	diags := []Diagnostic{
		projectDiagnostic(CodeProjectUnspecified, "Application argocd/demo uses a project diagnostic with future wording"),
		{
			Severity: SeverityWarning,
			Category: "project",
			Message:  "Application argocd/demo uses a project diagnostic without a stable code yet",
		},
		renderDiagnostic("Application argocd/demo failed to render: boom"),
	}

	got := FilterProjectDiagnostics(diags, ProjectDiagnosticsModeActionable)
	if len(got) != 1 {
		t.Fatalf("FilterProjectDiagnostics(actionable) len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Category != "render" {
		t.Fatalf("FilterProjectDiagnostics(actionable)[0] = %#v, want render diagnostic", got[0])
	}
}

func TestFilterProjectDiagnosticsAllKeepsEverything(t *testing.T) {
	diags := []Diagnostic{
		projectDiagnostic(CodeProjectResourceScopeDeferred, "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred"),
		projectDiagnostic(CodeProjectDestinationDenied, "Application argocd/demo destination is not permitted by AppProject \"platform\""),
		renderDiagnostic("Application argocd/demo failed to render: boom"),
	}

	got := FilterProjectDiagnostics(diags, ProjectDiagnosticsModeAll)
	if len(got) != len(diags) {
		t.Fatalf("FilterProjectDiagnostics(all) len = %d, want %d: %#v", len(got), len(diags), got)
	}
}

func TestFilterProjectDiagnosticsOffDropsOnlyProjectAdjacent(t *testing.T) {
	diags := []Diagnostic{
		projectDiagnostic(CodeProjectDestinationDenied, "Application argocd/demo destination is not permitted by AppProject \"platform\""),
		projectDiagnostic(CodeProjectUnspecified, "Application argocd/demo uses a project diagnostic with future wording"),
		{
			Code:     CodeRepositoryProjectMismatch,
			Severity: SeverityWarning,
			Category: "repository",
			Message:  "Application argocd/demo repository metadata for \"https://github.com/example/repo\" is scoped to project \"infra\", not AppProject \"platform\"",
		},
		{
			Code:     CodeClusterMetadataMissing,
			Severity: SeverityWarning,
			Category: "cluster",
			Message:  "Application argocd/demo is missing cluster metadata for AppProject validation",
		},
		{
			Code:     "repository.unspecified",
			Severity: SeverityWarning,
			Category: "repository",
			Message:  "repository warning unrelated to AppProject validation",
		},
		renderDiagnostic("Application argocd/demo failed to render: boom"),
	}

	got := FilterProjectDiagnostics(diags, ProjectDiagnosticsModeOff)
	if len(got) != 2 {
		t.Fatalf("FilterProjectDiagnostics(off) len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Code != "repository.unspecified" || got[1].Category != "render" {
		t.Fatalf("FilterProjectDiagnostics(off) = %#v, want non-project diagnostics only", got)
	}
}

func projectDiagnostic(code, message string) Diagnostic {
	return Diagnostic{
		Code:     code,
		Severity: SeverityWarning,
		Category: "project",
		Message:  message,
	}
}

func renderDiagnostic(message string) Diagnostic {
	return Diagnostic{
		Severity: SeverityError,
		Category: "render",
		Message:  message,
	}
}

func TestStableCodePluginFallbacks(t *testing.T) {
	diags := WithStableCodes([]Diagnostic{{
		Severity: SeverityError,
		Category: "plugin",
		Message:  "legacy plugin diagnostic",
	}})
	if got := diags[0].Code; got != CodePluginUnspecified {
		t.Fatalf("Code = %q, want %s", got, CodePluginUnspecified)
	}
	explicit := WithStableCodes([]Diagnostic{{
		Code:     CodePluginFailed,
		Severity: SeverityError,
		Category: "plugin",
		Message:  "explicit failure",
	}})
	if got := explicit[0].Code; got != CodePluginFailed {
		t.Fatalf("Code = %q, want %s", got, CodePluginFailed)
	}
}

func TestStableCodesIncludeProviderFixtureDiagnostics(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "invalid fixture",
			message: "provider fixture invalid: failed to decode fixture.yaml",
			want:    "appset.provider-fixture-invalid",
		},
		{
			name:    "no match",
			message: "provider fixture supplied but no entries match cluster generator",
			want:    "appset.provider-no-match",
		},
		{
			name:    "unsupported filter",
			message: "provider filter cannot be evaluated from fixture fields",
			want:    "appset.provider-unsupported-filter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := WithStableCodes([]Diagnostic{{
				Severity: SeverityWarning,
				Category: "appset",
				Message:  tt.message,
			}})
			if got := diags[0].Code; got != tt.want {
				t.Fatalf("Code = %q, want %q", got, tt.want)
			}
		})
	}
}
