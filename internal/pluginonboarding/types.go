package pluginonboarding

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const (
	PlaceholderImage   = "registry.example.invalid/drydock/plugin-placeholder@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	PlaceholderCommand = "TODO_REPLACE_WITH_DIRECT_EXECUTABLE"
	// PlaceholderCommandToken is the command token required by the onboarding
	// generator contract. PlaceholderCommand is kept as a shorter alias for
	// existing callers in this package.
	PlaceholderCommandToken = PlaceholderCommand

	SchemaModeline = "# yaml-language-server: $schema=https://raw.githubusercontent.com/sholdee/drydock/main/schemas/plugin-policy.schema.json"

	StatusPass = "PASS"
	StatusWarn = "WARN"
	StatusFail = "FAIL"
	StatusInfo = "INFO"
)

const (
	IssuePolicyMissing      = "policy.missing"
	IssuePolicyUntrusted    = "policy.untrusted"
	IssuePluginsDisabled    = "plugins.disabled"
	IssueImagePlaceholder   = "image.placeholder"
	IssueImageMutable       = "image.mutable"
	IssueImageAmbiguous     = "image.ambiguous"
	IssueCommandPlaceholder = "command.placeholder"
	IssueBootstrapMissing   = "bootstrap.missing"
	IssueParamsMissingAllow = "params.missing_allow"
	IssueEnvMissingAllow    = "env.missing_allow"
)

type ApplicationInput struct {
	Application argoappv1.Application
	Paths       []string
}

type BootstrapEntrypointHint struct {
	Plugin     string
	SourcePath string
}

type AnalyzeOptions struct {
	IncludeUnused        bool
	BootstrapEntrypoints []BootstrapEntrypointHint
}

type GenerateOptions struct {
	Engine                pluginpolicy.Engine
	EngineExplicit        bool
	Comments              bool
	AllowMutableImageTags bool
}

type DoctorOptions struct {
	EnablePlugins bool
	TrustedPolicy bool
	Strict        bool
}

type Report struct {
	Root                  string
	Plugins               []PluginReport
	Sidecars              []SidecarCandidate
	BootstrapEntrypoints  []BootstrapEntrypointHint
	ExistingPolicyPresent bool
}

type PluginReport struct {
	Name            string
	Used            bool
	Uses            []PluginUse
	CMP             *config.ConfigManagementPlugin
	Discover        *pluginpolicy.PluginDiscoverMatch
	Generate        []string
	GenerateSafe    bool
	SuggestedEngine pluginpolicy.Engine
	Parameters      []ParameterEvidence
	Env             []string
	Sidecar         SidecarMatch
}

type PluginUse struct {
	AppNamespace string `json:"appNamespace" yaml:"appNamespace"`
	AppName      string `json:"appName" yaml:"appName"`
	SourceIndex  int    `json:"sourceIndex" yaml:"sourceIndex"`
	SourcePath   string `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
	Explicit     bool   `json:"explicit" yaml:"explicit"`
	StaticMatch  bool   `json:"staticMatch" yaml:"staticMatch"`
}

type ParameterEvidence struct {
	Name string
	Type pluginpolicy.ExecParameterType
}

type SidecarConfidence string

const (
	SidecarConfidenceNone       SidecarConfidence = "none"
	SidecarConfidenceStructural SidecarConfidence = "structural"
	SidecarConfidenceSingle     SidecarConfidence = "single-candidate"
	SidecarConfidenceAmbiguous  SidecarConfidence = "ambiguous"
)

type SidecarCandidate struct {
	Name       string
	Image      string
	Source     string
	Provenance diagnostic.Provenance
	Signals    []string
}

type SidecarMatch struct {
	Confidence SidecarConfidence
	Candidate  *SidecarCandidate
	Candidates []SidecarCandidate
}

type ReadinessReport struct {
	Status          string                 `json:"status" yaml:"status"`
	Applications    []ApplicationReadiness `json:"applications" yaml:"applications"`
	Plugins         []PluginReadiness      `json:"plugins" yaml:"plugins"`
	Recommendations []ReadinessIssue       `json:"recommendations,omitempty" yaml:"recommendations,omitempty"`
}

type ApplicationReadiness struct {
	Namespace string           `json:"namespace" yaml:"namespace"`
	Name      string           `json:"name" yaml:"name"`
	Status    string           `json:"status" yaml:"status"`
	Issues    []ReadinessIssue `json:"issues,omitempty" yaml:"issues,omitempty"`
}

type PluginReadiness struct {
	Name   string           `json:"name" yaml:"name"`
	Status string           `json:"status" yaml:"status"`
	Issues []ReadinessIssue `json:"issues,omitempty" yaml:"issues,omitempty"`
}

type ReadinessIssue struct {
	Code    string `json:"code" yaml:"code"`
	Status  string `json:"status" yaml:"status"`
	Message string `json:"message" yaml:"message"`
	Plugin  string `json:"plugin,omitempty" yaml:"plugin,omitempty"`
}
