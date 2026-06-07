package app

import "github.com/sholdee/drydock/internal/diagnostic"

func (request BuildRequest) filterProjectDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	return diagnostic.FilterProjectDiagnostics(diags, request.ProjectDiagnosticsMode)
}

func (request BuildRequest) normalizeDiagnostics(diags []diagnostic.Diagnostic, forceWarning bool) []diagnostic.Diagnostic {
	return normalizeDiagnostics(request.filterProjectDiagnostics(diags), request.Strict, forceWarning)
}

func (request DiffRequest) filterProjectDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	return diagnostic.FilterProjectDiagnostics(diags, request.ProjectDiagnosticsMode)
}
