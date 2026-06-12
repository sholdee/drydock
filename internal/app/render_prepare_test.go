package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

type renderAndPrepareFailingProvider struct{}

func (renderAndPrepareFailingProvider) RenderSource(_ context.Context, source render.ResolvedSource, _ render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if source.Path == "first" {
		return nil, nil, fmt.Errorf("render boom")
	}
	return nil, nil, nil
}

func (renderAndPrepareFailingProvider) PrepareSource(_ context.Context, _ argoappv1.Application, sourcePlan SourcePlan) (SourcePlan, error) {
	if sourcePlan.Source.Path == "second" {
		return sourcePlan, fmt.Errorf("prepare boom")
	}
	return sourcePlan, nil
}

// An earlier source's render failure must surface instead of a later
// source's prepare failure — the prepared prefix renders first.
func TestRenderApplicationEarlierRenderFailureWinsOverLaterPrepareFailure(t *testing.T) {
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", Path: "first", TargetRevision: "main"},
		{RepoURL: "https://git.example.test/org/repo.git", Path: "second", TargetRevision: "main"},
	}}}

	_, err := RenderApplicationWithOptions(context.Background(), application, renderAndPrepareFailingProvider{}, ApplicationRenderOptions{TrackingOptions: defaultTrackingOptions()})
	if err == nil || !strings.Contains(err.Error(), "render boom") || strings.Contains(err.Error(), "prepare boom") {
		t.Fatalf("err = %v, want source[0] render failure to win over source[1] prepare failure", err)
	}
}

type prepareFailingProvider struct{}

func (prepareFailingProvider) RenderSource(_ context.Context, source render.ResolvedSource, _ render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return nil, []diagnostic.Diagnostic{{
		Severity: diagnostic.SeverityWarning,
		Category: "test",
		Message:  "warning from " + source.Path,
	}}, nil
}

func (prepareFailingProvider) PrepareSource(_ context.Context, _ argoappv1.Application, sourcePlan SourcePlan) (SourcePlan, error) {
	if sourcePlan.Source.Path == "second" {
		return sourcePlan, fmt.Errorf("prepare boom")
	}
	return sourcePlan, nil
}

func TestRenderApplicationPrepareFailurePreservesEarlierSourceDiagnostics(t *testing.T) {
	application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
		{RepoURL: "https://git.example.test/org/repo.git", Path: "first", TargetRevision: "main"},
		{RepoURL: "https://git.example.test/org/repo.git", Path: "second", TargetRevision: "main"},
	}}}
	handle := newPersistentRenderCache(testRenderCacheOptions(t), false)
	options := ApplicationRenderOptions{TrackingOptions: defaultTrackingOptions()}
	options.persistent = persistentRenderOptions{cache: handle}

	result, err := RenderApplicationWithOptions(context.Background(), application, prepareFailingProvider{}, options)
	if err == nil || !strings.Contains(err.Error(), "prepare boom") {
		t.Fatalf("err = %v, want prepare failure", err)
	}
	found := false
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "warning from first") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want source[0] warning preserved on source[1] prepare failure", result.Diagnostics)
	}
}
