package app

import (
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/labels"
)

// SelectorNoMatchCode identifies a label selector (-l, --selector) that
// matched no Application. The command still runs: the warning tells the
// user that an empty result comes from the selector, not from the
// repository.
const SelectorNoMatchCode = "selector.no-match"

// SelectorNoMatchDiagnostic is the SelectorNoMatchCode warning for selector.
func SelectorNoMatchDiagnostic(selector labels.Selector) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     SelectorNoMatchCode,
		Severity: diagnostic.SeverityWarning,
		Category: "selector",
		Message:  fmt.Sprintf("label selector %q matched no Application", selector.String()),
	}
}

// selectsByLabels reports whether selector narrows the Applications. A nil or
// empty selector selects every Application.
func selectsByLabels(selector labels.Selector) bool {
	return selector != nil && !selector.Empty()
}

// selectDiffSidesByLabels narrows the Applications that each side of a diff
// renders (leftApps, rightApps) to those that selector matches on
// Application metadata labels. An Application on one side only is matched by
// its labels on that side. An Application on both sides is kept on both sides
// when its labels on either side match, so a label change does not hide half
// of its diff. leftAll and rightAll are every Application of each side; they
// give the labels, also for an Application that changed-only did not select
// on that side. matched reports whether selector matched any Application.
func selectDiffSidesByLabels(selector labels.Selector, leftAll, rightAll, leftApps, rightApps []argoappv1.Application) (left, right []argoappv1.Application, matched bool) {
	keys := map[string]struct{}{}
	for _, applications := range [][]argoappv1.Application{leftAll, rightAll} {
		for _, application := range applications {
			if selector.Matches(labels.Set(application.Labels)) {
				keys[applicationKey(application)] = struct{}{}
			}
		}
	}
	return keepApplicationKeys(leftApps, keys), keepApplicationKeys(rightApps, keys), len(keys) > 0
}

// keepApplicationKeys returns the Applications whose applicationKey is in
// keys. The result is never nil, because a nil Applications list makes Build
// discover and render every Application.
func keepApplicationKeys(applications []argoappv1.Application, keys map[string]struct{}) []argoappv1.Application {
	out := make([]argoappv1.Application, 0, len(applications))
	for _, application := range applications {
		if _, ok := keys[applicationKey(application)]; ok {
			out = append(out, application)
		}
	}
	return out
}
