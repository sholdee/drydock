package app

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/labels"
)

// A label selector on a diff renders only the selected Applications. With
// changed-only, an Application is rendered only when it is selected and
// changed: beta is selected but unchanged, gamma is changed but not selected.
func TestOrchestratorDiffAppsLabelSelectorRendersOnlySelectedApplications(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeLabeledDiffApplication(t, left, "alpha", "a", "v1")
	writeLabeledDiffApplication(t, left, "beta", "a", "v1")
	writeLabeledDiffApplication(t, left, "gamma", "b", "v1")
	writeLabeledDiffApplication(t, right, "alpha", "a", "v2")
	writeLabeledDiffApplication(t, right, "beta", "a", "v1")
	writeLabeledDiffApplication(t, right, "gamma", "b", "v2")
	selector, err := labels.Parse("team=a")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		changedOnly bool
		want        []string
	}{
		{changedOnly: true, want: []string{"manifests/alpha"}},
		{changedOnly: false, want: []string{"manifests/alpha", "manifests/beta"}},
	} {
		var mu sync.Mutex
		rendered := map[string]struct{}{}
		o := Orchestrator{}
		o.renderObserver = func(source render.ResolvedSource) {
			mu.Lock()
			defer mu.Unlock()
			rendered[source.Path] = struct{}{}
		}
		result, err := o.DiffApps(context.Background(), DiffRequest{
			LeftPath:      left,
			RightPath:     right,
			Unified:       3,
			ChangedOnly:   tc.changedOnly,
			LabelSelector: selector,
		})
		if err != nil {
			t.Fatalf("changedOnly=%t: DiffApps() error = %v, diagnostics = %#v", tc.changedOnly, err, result.Diagnostics)
		}
		if got := slices.Sorted(maps.Keys(rendered)); !slices.Equal(got, tc.want) {
			t.Fatalf("changedOnly=%t: rendered %v, want %v", tc.changedOnly, got, tc.want)
		}
		if len(result.Results) != 1 || result.Results[0].Parent.Name != "alpha" {
			t.Fatalf("changedOnly=%t: results = %#v, want the alpha ConfigMap only", tc.changedOnly, result.Results)
		}
		if len(result.Diagnostics) != 0 {
			t.Fatalf("changedOnly=%t: diagnostics = %#v, want none", tc.changedOnly, result.Diagnostics)
		}
	}
}

func TestOrchestratorDiffAppsLabelSelectorWarnsWhenNoApplicationMatches(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeLabeledDiffApplication(t, left, "alpha", "a", "v1")
	writeLabeledDiffApplication(t, right, "alpha", "a", "v2")
	selector, err := labels.Parse("team=none")
	if err != nil {
		t.Fatal(err)
	}

	rendered := 0
	o := Orchestrator{}
	o.renderObserver = func(render.ResolvedSource) { rendered++ }
	result, err := o.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3, LabelSelector: selector})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if rendered != 0 || len(result.Results) != 0 {
		t.Fatalf("rendered %d sources and %d results, want none", rendered, len(result.Results))
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != SelectorNoMatchCode {
		t.Fatalf("diagnostics = %#v, want one %s warning", result.Diagnostics, SelectorNoMatchCode)
	}
}

// A settings change renders every Application under changed-only. With a
// selector, it renders every selected Application, and the warning says so.
func TestOrchestratorDiffAppsLabelSelectorNarrowsSettingsRenderAll(t *testing.T) {
	root := t.TempDir()
	for _, side := range []struct{ dir, settings string }{
		{filepath.Join(root, "left"), trackingAnnotationSettings},
		{filepath.Join(root, "right"), trackingLabelSettings},
	} {
		writeTestFile(t, filepath.Join(side.dir, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
  labels:
    team: platform
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: argocd/settings
  destination:
    name: in-cluster
    namespace: argocd
`)
		writeTestFile(t, filepath.Join(side.dir, "argocd", "settings", "argocd-cm.yaml"), argocdCMYAML(side.settings))
		writeLabeledDiffApplication(t, side.dir, "alpha", "a", "v1")
		writeLabeledDiffApplication(t, side.dir, "beta", "b", "v1")
	}
	selector, err := labels.Parse("team=a")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	rendered := map[string]struct{}{}
	o := Orchestrator{}
	o.renderObserver = func(source render.ResolvedSource) {
		mu.Lock()
		defer mu.Unlock()
		rendered[source.Path] = struct{}{}
	}
	result, err := o.DiffApps(context.Background(), DiffRequest{
		LeftPath:      filepath.Join(root, "left"),
		RightPath:     filepath.Join(root, "right"),
		Unified:       3,
		ChangedOnly:   true,
		Strict:        true,
		LabelSelector: selector,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got, want := slices.Sorted(maps.Keys(rendered)), []string{"manifests/alpha"}; !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}
	if got, want := diffedApplications(result), []string{"alpha"}; !slices.Equal(got, want) {
		t.Fatalf("diffed Applications = %v, want %v", got, want)
	}
	want := changedOnlySettingsDiagnostic()
	want.Message = "Argo CD settings changed; rendering all selected Applications"
	if !slices.Equal(result.Diagnostics, []diagnostic.Diagnostic{want}) {
		t.Fatalf("diagnostics = %#v, want %#v", result.Diagnostics, want)
	}
}

func TestSelectorNoMatchIsStrictExempt(t *testing.T) {
	selector, err := labels.Parse("team=none")
	if err != nil {
		t.Fatal(err)
	}
	diags := normalizeDiagnostics([]diagnostic.Diagnostic{SelectorNoMatchDiagnostic(selector)}, true, false)
	if diags[0].Severity != diagnostic.SeverityWarning {
		t.Fatalf("strict severity = %s, want warning", diags[0].Severity)
	}
	if err := diagnosticFailure(diags, true); err != nil {
		t.Fatalf("strict diagnosticFailure() = %v, want selector.no-match exempt", err)
	}
}

func TestSelectedRenderAllDiagnostics(t *testing.T) {
	scoped := changedOnlySettingsScopedDiagnostic(2)
	got := selectedRenderAllDiagnostics([]diagnostic.Diagnostic{
		changedOnlySettingsDiagnostic(),
		changedOnlyProjectsDiagnostic(),
		changedOnlySymlinksDiagnostic("", "escape.yaml"),
		scoped,
	})
	for _, diag := range got[:3] {
		if !strings.Contains(diag.Message, "rendering all selected Applications") {
			t.Fatalf("message = %q, want rendering all selected Applications", diag.Message)
		}
	}
	if got[3] != scoped {
		t.Fatalf("scoped diagnostic = %#v, want it unchanged", got[3])
	}
}

func TestSelectDiffSidesByLabels(t *testing.T) {
	labeled := func(name, team string) argoappv1.Application {
		return argoappv1.Application{Namespace: "argocd", Name: name, Labels: map[string]string{"team": team}}
	}
	leftAll := []argoappv1.Application{labeled("both", "a"), labeled("relabeled", "b"), labeled("left-only", "a"), labeled("other", "c")}
	rightAll := []argoappv1.Application{labeled("both", "a"), labeled("relabeled", "a"), labeled("right-only", "a"), labeled("other", "c"), labeled("right-other", "b")}
	selector, err := labels.Parse("team=a")
	if err != nil {
		t.Fatal(err)
	}
	names := func(applications []argoappv1.Application) []string {
		out := make([]string, 0, len(applications))
		for _, application := range applications {
			out = append(out, application.Name)
		}
		return out
	}

	left, right, matched := selectDiffSidesByLabels(selector, leftAll, rightAll, leftAll, rightAll)
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := names(left), []string{"both", "relabeled", "left-only"}; !slices.Equal(got, want) {
		t.Fatalf("left = %v, want %v", got, want)
	}
	if got, want := names(right), []string{"both", "relabeled", "right-only"}; !slices.Equal(got, want) {
		t.Fatalf("right = %v, want %v", got, want)
	}

	// Changed-only selected relabeled on the left side only. Its right-side
	// labels still select it, and an unselected Application stays out.
	left, right, _ = selectDiffSidesByLabels(selector, leftAll, rightAll, leftAll[1:2], nil)
	if got, want := names(left), []string{"relabeled"}; !slices.Equal(got, want) {
		t.Fatalf("changed-only left = %v, want %v", got, want)
	}
	if right == nil || len(right) != 0 {
		t.Fatalf("changed-only right = %#v, want an empty, non-nil list", right)
	}

	none, err := labels.Parse("team=none")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, matched := selectDiffSidesByLabels(none, leftAll, rightAll, leftAll, rightAll); matched {
		t.Fatal("matched = true for team=none, want false")
	}
}

// writeLabeledDiffApplication writes Application appName with the label team
// and a ConfigMap named appName whose value is value.
func writeLabeledDiffApplication(t *testing.T, root, appName, team, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
  labels:
    team: `+team+`
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+appName+`
data:
  value: `+value+`
`)
}
