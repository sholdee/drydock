package app

import (
	"errors"
	"fmt"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func expandApplicationSetDiscovery(root string, request BuildRequest, discovered discovery.Result, appsetOptions appset.Options) (discovery.Result, []diagnostic.Diagnostic, error) {
	var generated discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appSetFile := range discovered.ApplicationSets {
		apps, diags, err := appset.GenerateWithOptions(root, appSetFile.Path, appSetFile.ApplicationSet, appsetOptions)
		if err != nil {
			if errors.Is(err, appset.ErrUnsupportedGenerator) && len(diags) > 0 {
				allDiags = append(allDiags, normalizeDiagnostics(diags, request.Strict, true)...)
				continue
			}
			allDiags = append(allDiags, diags...)
			return discovered, allDiags, diagnosticsError(diags, err)
		}
		allDiags = append(allDiags, normalizeDiagnostics(diags, request.Strict, false)...)
		if len(apps) == 0 {
			if len(diags) != 0 {
				continue
			}
			diags := normalizeDiagnostics([]diagnostic.Diagnostic{emptyApplicationSetDiagnostic(appSetFile)}, request.Strict, false)
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
