package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func expandApplicationSetDiscovery(root string, request BuildRequest, discovered discovery.Result, appsetOptions appset.Options) (discovery.Result, []diagnostic.Diagnostic, error) {
	var generated discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appSetFile := range discovered.ApplicationSets {
		apps, diags, err := generateApplicationSetCached(request.appsetGenerationMemo, root, appSetFile, appsetOptions)
		if err != nil {
			if errors.Is(err, appset.ErrUnsupportedGenerator) && len(diags) > 0 {
				allDiags = append(allDiags, request.normalizeDiagnostics(diags, true)...)
				continue
			}
			allDiags = append(allDiags, diags...)
			return discovered, allDiags, diagnosticsError(diags, err)
		}
		allDiags = append(allDiags, request.normalizeDiagnostics(diags, false)...)
		if len(apps) == 0 {
			if len(diags) != 0 {
				continue
			}
			diags := request.normalizeDiagnostics([]diagnostic.Diagnostic{emptyApplicationSetDiagnostic(appSetFile)}, false)
			allDiags = append(allDiags, diags...)
			if err := diagnosticFailure(diags, request.Strict); err != nil {
				return discovered, allDiags, err
			}
			continue
		}
		for _, app := range apps {
			generated.Applications = append(generated.Applications, discovery.ApplicationFile{
				Path:          appSetFile.Path,
				DocumentIndex: appSetFile.DocumentIndex,
				Application:   app.Application,
				Tier:          appSetFile.Tier,
				InputPaths:    generatedApplicationInputPaths(appSetFile, app),
			})
		}
	}
	merged, mergeDiags := mergeDiscoveryResultsWithDiagnostics(discovered, generated)
	allDiags = append(allDiags, mergeDiags...)
	return merged, allDiags, nil
}

type appsetGenerationOutcome struct {
	apps  []appset.GeneratedApplication
	diags []diagnostic.Diagnostic
	err   error
}

func generateApplicationSetCached(memo *sync.Map, root string, appSetFile discovery.ApplicationSetFile, appsetOptions appset.Options) ([]appset.GeneratedApplication, []diagnostic.Diagnostic, error) {
	key, keyErr := appsetGenerationMemoKey(appSetFile, appsetOptions)
	if memo == nil || keyErr != nil {
		return appset.GenerateWithOptions(root, appSetFile.Path, appSetFile.ApplicationSet, appsetOptions)
	}
	if cached, ok := memo.Load(key); ok {
		if outcome, ok := cached.(appsetGenerationOutcome); ok {
			return copyGeneratedApplications(outcome.apps), copyDiagnostics(outcome.diags), outcome.err
		}
	}
	apps, diags, err := appset.GenerateWithOptions(root, appSetFile.Path, appSetFile.ApplicationSet, appsetOptions)
	memo.Store(key, appsetGenerationOutcome{
		apps:  copyGeneratedApplications(apps),
		diags: copyDiagnostics(diags),
		err:   err,
	})
	return copyGeneratedApplications(apps), copyDiagnostics(diags), err
}

func appsetGenerationMemoKey(appSetFile discovery.ApplicationSetFile, appsetOptions appset.Options) (string, error) {
	content, err := json.Marshal(struct {
		ApplicationSet any            `json:"applicationSet"`
		Options        appset.Options `json:"options"`
	}{
		ApplicationSet: appSetFile.ApplicationSet,
		Options:        appsetOptions,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%s|%d|%x", appSetFile.Path, appSetFile.DocumentIndex, sum[:]), nil
}

func copyGeneratedApplications(apps []appset.GeneratedApplication) []appset.GeneratedApplication {
	if len(apps) == 0 {
		return nil
	}
	out := make([]appset.GeneratedApplication, len(apps))
	for i, app := range apps {
		out[i] = app
		if clone := app.Application.DeepCopy(); clone != nil {
			out[i].Application = *clone
		}
		out[i].SourcePaths = append([]string(nil), app.SourcePaths...)
	}
	return out
}

func copyDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	out := make([]diagnostic.Diagnostic, len(diags))
	copy(out, diags)
	return out
}

func emptyApplicationSetDiagnostic(appSetFile discovery.ApplicationSetFile) diagnostic.Diagnostic {
	name := appSetFile.ApplicationSet.Name
	if appSetFile.ApplicationSet.Namespace != "" {
		name = appSetFile.ApplicationSet.Namespace + "/" + name
	}
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s generated zero Applications", name),
		Provenance: diagnostic.Provenance{Path: appSetFile.Path, Pointer: "spec.generators"},
	}
}
