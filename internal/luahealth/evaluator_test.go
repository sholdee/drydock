package luahealth

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEvaluatorValidHealthStatesDoNotDiagnose(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Progressing", message = "waiting for status" }`,
		},
		"example.com/Gadget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests: []render.Manifest{
			{Object: object("example.com/v1", "Widget", "default", "demo")},
			{Object: object("example.com/v1", "Gadget", "default", "demo")},
		},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestEvaluatorReportsSyntaxErrorForMatchingResourceWithoutSource(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua:    true,
			HealthLua:       `return { status = "Healthy", message = "super-secret" `,
			HealthLuaSHA256: "hash",
			Provenance:      diagnostic.Provenance{Path: "apps/argocd/values.yaml", Pointer: "configs.cm.resource.customizations.health.example.com_Widget"},
		},
	}})

	unmatchedDiags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Gadget", "default", "demo")}},
	})
	if len(unmatchedDiags) != 0 {
		t.Fatalf("unmatched diagnostics = %#v, want none", unmatchedDiags)
	}

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-compile-failed", "failed to compile health Lua")
	if got := diags[0].Provenance.Path; got != "apps/argocd/values.yaml" {
		t.Fatalf("Provenance.Path = %q, want settings path", got)
	}
	assertMessageDoesNotContain(t, diags[0], "super-secret")
}

func TestEvaluatorReportsRuntimeErrorForMatchingResource(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return obj.status.conditions[1].type`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-failed", "health Lua failed for Application argocd/demo resource example.com/Widget default/demo")
	assertMessageContains(t, diags[0], "runtime error")
}

func TestEvaluatorReportsInvalidReturnType(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return "Healthy"`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-invalid-return", "expect table output from Lua script")
}

func TestEvaluatorReportsInvalidHealthStatus(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "AlmostHealthy" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-invalid-status", "Lua returned an invalid health status")
}

func TestEvaluatorAllowsReservedInvalidStatusMessageWhenStatusIsValid(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy", message = "Lua returned an invalid health status" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestEvaluatorTreatsReservedInvalidStatusErrorPayloadAsRuntimeError(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `error("Lua returned an invalid health status")`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-failed", "runtime error")
	assertMessageDoesNotContain(t, diags[0], "Lua returned an invalid health status")
}

func TestEvaluatorHonorsUseOpenLibs(t *testing.T) {
	settings := config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua:   true,
			HealthLua:      `local _ = string.len("abc"); return { status = "Healthy" }`,
			HasUseOpenLibs: true,
			UseOpenLibs:    true,
		},
	}}

	evaluator := New(settings)
	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestEvaluatorReportsUseOpenLibsDisabledRuntimeError(t *testing.T) {
	settings := config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `local _ = string.len("abc"); return { status = "Healthy" }`,
		},
	}}

	evaluator := New(settings)
	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-failed", "health Lua failed")
}

func TestEvaluatorMatchesWildcard(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/*": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestEvaluatorExactMatchWinsOverWildcard(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/*": {
			HasHealthLua: true,
			HealthLua:    `return obj.status.conditions[1].type`,
		},
		"example.com/Widget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestEvaluatorReportsAmbiguousWildcardMatches(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"example.com/*": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy" }`,
		},
		"*/Widget": {
			HasHealthLua: true,
			HealthLua:    `return { status = "Healthy" }`,
		},
	}})

	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-ambiguous-customization", "ambiguous health Lua customizations")
	assertMessageContains(t, diags[0], "*/Widget, example.com/*")
}

func TestEvaluatorDoesNotExposeLuaSource(t *testing.T) {
	for _, script := range []string{
		`error("token=super-secret")`,
		`error("health.lua:super-secret")`,
	} {
		t.Run(script, func(t *testing.T) {
			evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
				"example.com/Widget": {
					HasHealthLua: true,
					HealthLua:    script,
				},
			}})

			diags := evaluator.Validate(context.Background(), Request{
				Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
				Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
			})
			assertOneHealthDiagnostic(t, diags, "health.lua-failed", "health Lua failed")
			assertMessageDoesNotContain(t, diags[0], "super-secret")
			assertMessageDoesNotContain(t, diags[0], script)
		})
	}
}

func TestEvaluatorDoesNotExposeSecretManifestValueFromLuaError(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"Secret": {
			HasHealthLua: true,
			HealthLua:    `error(obj.data.token)`,
		},
	}})

	secret := object("v1", "Secret", "default", "demo")
	secret.Object["data"] = map[string]any{"token": "super-secret"}
	diags := evaluator.Validate(context.Background(), Request{
		Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
		Manifests:   []render.Manifest{{Object: secret}},
	})
	assertOneHealthDiagnostic(t, diags, "health.lua-failed", "health Lua failed")
	assertMessageDoesNotContain(t, diags[0], "super-secret")
}

func TestEvaluatorSuppressesLuaPrintOutput(t *testing.T) {
	evaluator := New(config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{
		"Secret": {
			HasHealthLua: true,
			HealthLua:    `print(obj.data.token); return { status = "Healthy" }`,
		},
	}})

	secret := object("v1", "Secret", "default", "demo")
	secret.Object["data"] = map[string]any{"token": "super-secret"}
	var diags []diagnostic.Diagnostic
	output := captureProcessStdout(t, func() {
		diags = evaluator.Validate(context.Background(), Request{
			Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
			Manifests:   []render.Manifest{{Object: secret}},
		})
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	if output != "" {
		t.Fatalf("process stdout = %q, want suppressed Lua print output", output)
	}
}

func object(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
	}}
}

func assertOneHealthDiagnostic(t *testing.T, diags []diagnostic.Diagnostic, code, fragment string) {
	t.Helper()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diags)
	}
	if diags[0].Severity != diagnostic.SeverityError {
		t.Fatalf("Severity = %q, want error", diags[0].Severity)
	}
	if diags[0].Category != "health" {
		t.Fatalf("Category = %q, want health", diags[0].Category)
	}
	if diags[0].Code != code {
		t.Fatalf("Code = %q, want %q", diags[0].Code, code)
	}
	assertMessageContains(t, diags[0], fragment)
}

func assertMessageContains(t *testing.T, diag diagnostic.Diagnostic, fragment string) {
	t.Helper()
	if !strings.Contains(diag.Message, fragment) {
		t.Fatalf("diagnostic message = %q, want fragment %q", diag.Message, fragment)
	}
}

func assertMessageDoesNotContain(t *testing.T, diag diagnostic.Diagnostic, fragment string) {
	t.Helper()
	if strings.Contains(diag.Message, fragment) {
		t.Fatalf("diagnostic message = %q, must not contain %q", diag.Message, fragment)
	}
}

func captureProcessStdout(t *testing.T, fn func()) (output string) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, reader)
		_ = reader.Close()
		close(done)
	}()

	os.Stdout = writer
	defer func() {
		os.Stdout = original
		_ = writer.Close()
		<-done
		output = buf.String()
	}()
	fn()
	return output
}
