package luahealth

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/argoproj/argo-cd/gitops-engine/pkg/health"
	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	argolua "github.com/argoproj/argo-cd/v3/util/lua"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const invalidHealthStatusMessage = "Lua returned an invalid health status"

var luaStdoutMu sync.Mutex

type ApplicationRef struct {
	Namespace string
	Name      string
}

type Request struct {
	Application ApplicationRef
	Manifests   []render.Manifest
}

type Evaluator struct {
	customizations []customization
}

type customization struct {
	key         string
	healthLua   string
	useOpenLibs bool
	provenance  diagnostic.Provenance
	compileErr  bool
}

func New(settings config.ArgoSettings) Evaluator {
	keys := make([]string, 0, len(settings.ResourceCustomizations))
	for key, resourceCustomization := range settings.ResourceCustomizations {
		if resourceCustomization.HasHealthLua && strings.TrimSpace(resourceCustomization.HealthLua) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	customizations := make([]customization, 0, len(keys))
	for _, key := range keys {
		resourceCustomization := settings.ResourceCustomizations[key]
		next := customization{
			key:         key,
			healthLua:   resourceCustomization.HealthLua,
			useOpenLibs: resourceCustomization.UseOpenLibs,
			provenance:  resourceCustomization.Provenance,
		}
		if err := compileHealthLua(next.healthLua); err != nil {
			next.compileErr = true
		}
		customizations = append(customizations, next)
	}

	return Evaluator{customizations: customizations}
}

func (e Evaluator) Validate(ctx context.Context, request Request) []diagnostic.Diagnostic {
	if len(e.customizations) == 0 {
		return nil
	}

	var diags []diagnostic.Diagnostic
	for _, renderedManifest := range request.Manifests {
		if ctx.Err() != nil {
			return diagnostic.WithStableCodes(diags)
		}

		matches := e.matches(renderedManifest)
		switch len(matches) {
		case 0:
			continue
		case 1:
			if matches[0].compileErr {
				diags = append(diags, compileFailureDiagnostic(request.Application, renderedManifest, matches[0]))
				continue
			}
			diag := evaluate(request.Application, renderedManifest, matches[0])
			if diag != nil {
				diags = append(diags, *diag)
			}
		default:
			diags = append(diags, ambiguousDiagnostic(request.Application, renderedManifest, matches))
		}
	}
	return diagnostic.WithStableCodes(diags)
}

func compileHealthLua(script string) error {
	wrapped := "if false then\n" + script + "\nend"
	_, err := argolua.VM{}.ExecuteHealthLua(&unstructured.Unstructured{}, wrapped)
	return err
}

func (e Evaluator) matches(renderedManifest render.Manifest) []customization {
	gvk := renderedManifest.Object.GroupVersionKind()
	key := argolua.GetConfigMapKey(gvk)
	for _, candidate := range e.customizations {
		if candidate.key == key {
			return []customization{candidate}
		}
	}

	var matches []customization
	for _, candidate := range e.customizations {
		if candidate.key != key && argoglob.Match(candidate.key, key) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func compileFailureDiagnostic(application ApplicationRef, renderedManifest render.Manifest, customization customization) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "health",
		Message: fmt.Sprintf(
			"failed to compile health Lua for Application %s resource %s using customization %q: syntax error",
			applicationName(application),
			resourceName(renderedManifest),
			customization.key,
		),
		Provenance: customization.provenance,
	}
}

func evaluate(application ApplicationRef, renderedManifest render.Manifest, customization customization) *diagnostic.Diagnostic {
	vm := argolua.VM{UseOpenLibs: customization.useOpenLibs}
	status, err := executeHealthLuaSilently(vm, renderedManifest.Object, customization.healthLua)
	if err != nil {
		diag := healthFailureDiagnostic(application, renderedManifest, customization.key, luaErrorReason(err))
		diag.Provenance = customization.provenance
		return &diag
	}
	if status != nil && status.Status == health.HealthStatusUnknown && status.Message == invalidHealthStatusMessage {
		diag := healthFailureDiagnostic(application, renderedManifest, customization.key, invalidHealthStatusMessage)
		diag.Provenance = customization.provenance
		return &diag
	}
	return nil
}

func executeHealthLuaSilently(vm argolua.VM, obj *unstructured.Unstructured, script string) (*health.HealthStatus, error) {
	luaStdoutMu.Lock()
	reader, writer, err := os.Pipe()
	if err != nil {
		luaStdoutMu.Unlock()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
		close(done)
	}()

	// gopher-lua's base print writes to process stdout directly. Argo CD's VM
	// does not expose a print hook, so redirect only while user health Lua runs.
	original := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = original
		_ = writer.Close()
		<-done
		luaStdoutMu.Unlock()
	}()
	return vm.ExecuteHealthLua(obj, script)
}

func healthFailureDiagnostic(application ApplicationRef, renderedManifest render.Manifest, customizationKey, reason string) diagnostic.Diagnostic {
	message := fmt.Sprintf(
		"health Lua failed for Application %s resource %s",
		applicationName(application),
		resourceName(renderedManifest),
	)
	if customizationKey != "" {
		message += fmt.Sprintf(" using customization %q", customizationKey)
	}
	message += ": " + reason
	return diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "health",
		Message:  message,
	}
}

func ambiguousDiagnostic(application ApplicationRef, renderedManifest render.Manifest, matches []customization) diagnostic.Diagnostic {
	keys := make([]string, 0, len(matches))
	for _, match := range matches {
		keys = append(keys, match.key)
	}
	sort.Strings(keys)

	return diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "health",
		Message: fmt.Sprintf(
			"ambiguous health Lua customizations for Application %s resource %s: %s",
			applicationName(application),
			resourceName(renderedManifest),
			strings.Join(keys, ", "),
		),
	}
}

func applicationName(application ApplicationRef) string {
	if application.Namespace == "" {
		return application.Name
	}
	return application.Namespace + "/" + application.Name
}

func resourceName(renderedManifest render.Manifest) string {
	return manifest.IdentityOf(renderedManifest.Object).String()
}

func luaErrorReason(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "expect table output from Lua script"):
		return "expect table output from Lua script"
	case strings.Contains(message, "context deadline exceeded"):
		return "execution timed out"
	default:
		return "runtime error"
	}
}
