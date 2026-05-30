package drydock

import (
	"maps"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
)

// Application identifies an Argo CD Application.
type Application struct {
	Namespace string
	Name      string
	Project   string
}

// Manifest is one rendered Kubernetes object with source provenance.
type Manifest struct {
	Application Application
	SourceIndex int
	SourceName  string
	SourcePath  string
	Object      map[string]any
}

// Diagnostic describes a warning, error, or informational finding.
type Diagnostic struct {
	Code       string
	Severity   string
	Category   string
	Message    string
	Provenance Provenance
}

// Provenance identifies where a Diagnostic originated.
type Provenance struct {
	Path    string
	Pointer string
}

// ApplicationStatus reports whether rendering an Application passed, failed, or
// was skipped.
type ApplicationStatus struct {
	Application Application
	Status      string
	Message     string
}

// CacheEvent describes an optional source acquisition cache observation.
type CacheEvent struct {
	Source   string
	Action   string
	Target   string
	Revision string
	CacheHit bool
	Offline  bool
	Refresh  bool
	Error    string
}

// PluginExecution describes one trusted exec plugin command that ran.
type PluginExecution struct {
	Application Application
	SourceIndex int
	SourceName  string
	SourcePath  string
	PluginName  string
	Engine      string
	Phase       string
	Command     string
	Duration    string
}

// RenderResult is returned by render operations.
type RenderResult struct {
	Applications     []Application
	Manifests        []Manifest
	Diagnostics      []Diagnostic
	Statuses         []ApplicationStatus
	CacheEvents      []CacheEvent
	PluginExecutions []PluginExecution
}

// ListApplicationsResult is returned by list operations.
type ListApplicationsResult struct {
	Applications []Application
	Diagnostics  []Diagnostic
	CacheEvents  []CacheEvent
}

// DiffApplicationsResult is returned by Application diff operations.
type DiffApplicationsResult struct {
	Results     []DiffResult
	Diagnostics []Diagnostic
	CacheEvents []CacheEvent
}

// DiffResult describes one resource-level Application diff.
type DiffResult struct {
	Parent   DiffParent
	Resource Resource
	Change   string
	Diff     string
}

// DiffParent identifies the Application source that produced a diff.
type DiffParent struct {
	Namespace   string
	Name        string
	SourceIndex int
	SourceName  string
	SourcePath  string
}

// Resource identifies a Kubernetes resource.
type Resource struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// ImageDiffResult is returned by image diff operations.
type ImageDiffResult struct {
	Added       []string
	Removed     []string
	Unchanged   []string
	Diagnostics []Diagnostic
	CacheEvents []CacheEvent
}

func renderResultFromBuild(result app.BuildResult) RenderResult {
	return RenderResult{
		Applications:     applicationsFromInternal(result.Applications),
		Manifests:        manifestsFromInternal(result.ApplicationManifests),
		Diagnostics:      diagnosticsFromInternal(result.Diagnostics),
		Statuses:         statusesFromInternal(result.Statuses),
		CacheEvents:      cacheEventsFromInternal(result.CacheEvents),
		PluginExecutions: pluginExecutionsFromInternal(result.PluginExecutions),
	}
}

func applicationsFromInternal(applications []argoappv1.Application) []Application {
	out := make([]Application, 0, len(applications))
	for _, application := range applications {
		out = append(out, Application{
			Namespace: application.Namespace,
			Name:      application.Name,
			Project:   application.Spec.Project,
		})
	}
	return out
}

func manifestsFromInternal(manifests []app.ApplicationManifest) []Manifest {
	out := make([]Manifest, 0, len(manifests))
	for _, item := range manifests {
		out = append(out, Manifest{
			Application: Application{
				Namespace: item.Application.Namespace,
				Name:      item.Application.Name,
				Project:   item.Application.Spec.Project,
			},
			SourceIndex: item.Manifest.SourceIndex,
			SourceName:  item.Manifest.SourceName,
			SourcePath:  item.Manifest.Path,
			Object:      cloneMap(item.Manifest.Object.Object),
		})
	}
	return out
}

func diagnosticsFromInternal(diagnostics []diagnostic.Diagnostic) []Diagnostic {
	normalized := diagnostic.WithStableCodes(diagnostics)
	out := make([]Diagnostic, 0, len(normalized))
	for _, item := range normalized {
		out = append(out, Diagnostic{
			Code:     item.Code,
			Severity: string(item.Severity),
			Category: item.Category,
			Message:  item.Message,
			Provenance: Provenance{
				Path:    item.Provenance.Path,
				Pointer: item.Provenance.Pointer,
			},
		})
	}
	return out
}

func diagnosticsToInternal(diagnostics []Diagnostic) []diagnostic.Diagnostic {
	out := make([]diagnostic.Diagnostic, 0, len(diagnostics))
	for _, item := range diagnostics {
		out = append(out, diagnostic.Diagnostic{
			Code:     item.Code,
			Severity: diagnostic.Severity(item.Severity),
			Category: item.Category,
			Message:  item.Message,
			Provenance: diagnostic.Provenance{
				Path:    item.Provenance.Path,
				Pointer: item.Provenance.Pointer,
			},
		})
	}
	return out
}

func statusesFromInternal(statuses []app.ApplicationStatus) []ApplicationStatus {
	out := make([]ApplicationStatus, 0, len(statuses))
	for _, item := range statuses {
		out = append(out, ApplicationStatus{
			Application: Application{
				Namespace: item.Namespace,
				Name:      item.Name,
			},
			Status:  item.Status,
			Message: item.Message,
		})
	}
	return out
}

func cacheEventsFromInternal(events []cacheevent.Event) []CacheEvent {
	out := make([]CacheEvent, 0, len(events))
	for _, event := range events {
		out = append(out, CacheEvent{
			Source:   string(event.Source),
			Action:   string(event.Action),
			Target:   event.Target,
			Revision: event.Revision,
			CacheHit: event.CacheHit,
			Offline:  event.Offline,
			Refresh:  event.Refresh,
			Error:    event.Error,
		})
	}
	return out
}

func pluginExecutionsFromInternal(executions []app.PluginExecution) []PluginExecution {
	out := make([]PluginExecution, 0, len(executions))
	for _, execution := range executions {
		out = append(out, PluginExecution{
			Application: Application{
				Namespace: execution.AppNamespace,
				Name:      execution.AppName,
			},
			SourceIndex: execution.SourceIndex,
			SourceName:  execution.SourceName,
			SourcePath:  execution.SourcePath,
			PluginName:  execution.PluginName,
			Engine:      execution.Engine,
			Phase:       execution.Phase,
			Command:     execution.Command,
			Duration:    execution.Duration,
		})
	}
	return out
}

func diffResultsFromInternal(results []diff.Result) []DiffResult {
	out := make([]DiffResult, 0, len(results))
	for _, item := range results {
		out = append(out, DiffResult{
			Parent: DiffParent{
				Namespace:   item.Parent.Namespace,
				Name:        item.Parent.Name,
				SourceIndex: item.Parent.SourceIndex,
				SourceName:  item.Parent.SourceName,
				SourcePath:  item.Parent.SourcePath,
			},
			Resource: Resource{
				Group:     item.Resource.Group,
				Kind:      item.Resource.Kind,
				Namespace: item.Resource.Namespace,
				Name:      item.Resource.Name,
			},
			Change: string(item.Change),
			Diff:   item.Diff,
		})
	}
	return out
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	maps.Copy(out, input)
	return out
}

func cloneAnyMaps(input []map[string]any) []map[string]any {
	if input == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneMap(item))
	}
	return out
}
