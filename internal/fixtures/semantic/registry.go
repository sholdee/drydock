package semantic

// Status describes how a semantic remediation fixture should be interpreted.
type Status string

const (
	StatusPending            Status = "pending"
	StatusActive             Status = "active"
	StatusDocumentedBoundary Status = "documented-boundary"
)

// Case describes a fixture created for semantic remediation work.
type Case struct {
	ID                string
	Phase             string
	Category          string
	FixturePath       string
	Status            Status
	Reason            string
	VerificationScope string
}

// Cases returns the semantic remediation fixture inventory.
func Cases() []Case {
	return []Case{
		{
			ID:                "SRC-EXPLICIT-CONFLICT",
			Phase:             "1",
			Category:          "source-selection",
			FixturePath:       "testdata/semantic-remediation/source-selection/explicit-conflict",
			Status:            StatusActive,
			Reason:            "R1 source conflict validation is covered by Phase 1 tests.",
			VerificationScope: "go test ./internal/app -run ExplicitSource",
		},
		{
			ID:                "SRC-DIRECTORY-EXPLICIT",
			Phase:             "1",
			Category:          "source-selection",
			FixturePath:       "testdata/semantic-remediation/source-selection/explicit-directory",
			Status:            StatusActive,
			Reason:            "R2 explicit directory source selection is covered by Phase 1 tests.",
			VerificationScope: "go test ./internal/app ./internal/render -run ExplicitSource",
		},
		{
			ID:                "SRC-DISCOVERY-PRECEDENCE",
			Phase:             "1",
			Category:          "source-selection",
			FixturePath:       "testdata/semantic-remediation/source-selection/discovery-precedence",
			Status:            StatusActive,
			Reason:            "R3 discovery precedence is covered by Phase 1 tests.",
			VerificationScope: "go test ./internal/app -run DiscoveryPrecedence",
		},
		{
			ID:                "SRC-ARGOCD-SOURCE",
			Phase:             "1",
			Category:          "source-overrides",
			FixturePath:       "testdata/semantic-remediation/source-overrides/basic",
			Status:            StatusActive,
			Reason:            "R5 source override loading is covered by Phase 1 tests.",
			VerificationScope: "go test ./internal/app -run ArgocdSource",
		},
		{
			ID:                "KUST-SOURCE-OPTIONS",
			Phase:             "2",
			Category:          "source-kustomize",
			FixturePath:       "testdata/semantic-remediation/source-kustomize/options",
			Status:            StatusActive,
			Reason:            "R29/R30/R31/R34/B12 source Kustomize options and versioned settings diagnostics are covered by Phase 2 tests.",
			VerificationScope: "go test ./internal/app ./internal/render -run SourceKustomize",
		},
		{
			ID:                "HELM-SOURCE-OPTIONS",
			Phase:             "3",
			Category:          "helm-source-options",
			FixturePath:       "testdata/semantic-remediation/helm-source-options/local-chart",
			Status:            StatusActive,
			Reason:            "R15/R18/R19/R22 Helm source options, value files, file parameters, schema validation, passCredentials, and dependency boundaries are covered by Phase 3 tests.",
			VerificationScope: "go test ./internal/render ./internal/app -run 'Helm.*(Value|Parameter|FileParameter|Schema|Glob|Dependency|PassCredentials)|CleanHelmSetParameter'",
		},
		{
			ID:                "TRACKING-METADATA",
			Phase:             "4",
			Category:          "tracking",
			FixturePath:       "testdata/semantic-remediation/tracking/basic",
			Status:            StatusActive,
			Reason:            "B2/B5/B7/B8/B9 tracking injection, installation ID parsing, and cache signatures are covered by Phase 4 tests.",
			VerificationScope: "go test ./internal/app ./internal/config -run 'Tracking|InstallationID|RenderCache|InstanceName'",
		},
		{
			ID:                "APPSET-TEMPLATE-PATCH",
			Phase:             "5",
			Category:          "appset-template-patch",
			FixturePath:       "testdata/semantic-remediation/appset-template-patch/basic",
			Status:            StatusActive,
			Reason:            "A17 templatePatch rendering and strategic merge behavior are covered by Phase 5 tests.",
			VerificationScope: "go test ./internal/appset -run TemplatePatch",
		},
		{
			ID:                "DIR-JSONNET-EDGES",
			Phase:             "6",
			Category:          "directory-jsonnet",
			FixturePath:       "testdata/semantic-remediation/directory-jsonnet/edges",
			Status:            StatusActive,
			Reason:            "R6/R8/R9/R11/R12 directory, skip marker, and Jsonnet behavior are covered by Phase 6 tests.",
			VerificationScope: "go test ./internal/render ./internal/app -run 'Directory|Jsonnet'",
		},
		{
			ID:                "CACHE-ROOT-SAFETY",
			Phase:             "4",
			Category:          "cache-safety",
			FixturePath:       "testdata/semantic-remediation/cache-safety/chart-remote",
			Status:            StatusActive,
			Reason:            "Chart and remote cache root validation is covered by Phase 4 tests across acquisition, render, build, and diff paths.",
			VerificationScope: "go test ./internal/app ./internal/chart ./internal/remote ./internal/acquisition ./internal/render -run 'Cache|Forbidden|ChartCache|RemoteCache|ForbiddenRoots'",
		},
		{
			ID:                "PLUGIN-NATIVE-KUSTOMIZE",
			Phase:             "7",
			Category:          "plugin-boundaries",
			FixturePath:       "testdata/semantic-remediation/plugin-boundaries/native-kustomize",
			Status:            StatusActive,
			Reason:            "R35/R36 native Kustomize CMP behavior and bounded sidecar auto-discovery diagnostics are covered by Phase 7 tests.",
			VerificationScope: "go test ./internal/app ./internal/pluginpolicy -run 'Plugin|CMP'",
		},
		{
			ID:                "PLUGIN-AVP-COMPAT",
			Phase:             "7",
			Category:          "plugin-boundaries",
			FixturePath:       "testdata/semantic-remediation/plugin-boundaries/avp-compat",
			Status:            StatusActive,
			Reason:            "AVP compatibility remains drydock-native redaction, with explicit policy coverage and no AVP execution.",
			VerificationScope: "go test ./internal/app -run AVP",
		},
		{
			ID:                "PROVIDER-ALL",
			Phase:             "5",
			Category:          "provider-fixtures",
			FixturePath:       "testdata/semantic-remediation/provider-fixtures/all-provider",
			Status:            StatusActive,
			Reason:            "Provider-backed ApplicationSet CLI fixture coverage spans every supported fixture family.",
			VerificationScope: "go test ./internal/appset ./internal/cli -run ProviderFixture",
		},
		{
			ID:                "SETTINGS-DIAGNOSTICS",
			Phase:             "7",
			Category:          "settings-diagnostics",
			FixturePath:       "testdata/semantic-remediation/settings-diagnostics/runtime-boundaries",
			Status:            StatusActive,
			Reason:            "B15/B19 cluster Secret metadata and argocd-cmd-params-cm runtime-boundary diagnostics are covered by Phase 7 tests.",
			VerificationScope: "go test ./internal/discovery ./internal/config ./internal/project ./internal/cli -run 'ClusterSecret|CmdParams|Settings|Diag'",
		},
		{
			ID:                "REMOTE-KUSTOMIZE-CACHE",
			Phase:             "8",
			Category:          "remote-kustomize-cache",
			FixturePath:       "testdata/semantic-remediation/remote-kustomize-cache/seeded-diff",
			Status:            StatusActive,
			Reason:            "Seeded remote Kustomize cache A/B diff coverage is exercised by a portable offline CLI fixture test.",
			VerificationScope: "go test ./internal/cli ./internal/remote ./internal/app -run RemoteKustomize",
		},
	}
}
