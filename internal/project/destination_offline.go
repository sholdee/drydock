package project

import (
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/remote"
)

// tristate is a Kleene truth value: an AppProject destination decision that
// depends on a cluster name or server drydock cannot see offline is
// tristateUnknown. The encoding orders the values so that AND is min and OR
// is max.
type tristate uint8

const (
	tristateFalse tristate = iota
	tristateUnknown
	tristateTrue
)

func tristateOf(value bool) tristate {
	if value {
		return tristateTrue
	}
	return tristateFalse
}

func (t tristate) and(other tristate) tristate { return min(t, other) }

func (t tristate) or(other tristate) tristate { return max(t, other) }

func (t tristate) not() tristate { return tristateTrue - t }

// offlineDestination is an Application destination whose cluster no
// discovered cluster Secret describes: a name-only destination, whose server
// is unknown, or a server-only destination, whose cluster name is unknown.
// An empty field is unknown. Argo CD itself always evaluates a resolved
// cluster, which carries a non-empty name and server.
type offlineDestination struct {
	name   string
	server string
}

// unresolvedOfflineDestination returns the offline view of dest when its
// cluster is unresolved and exactly one of name and server is set. A
// destination with both or neither is one Argo CD rejects before project
// policy; it keeps the direct IsDestinationPermitted evaluation.
func unresolvedOfflineDestination(dest argoappv1.ApplicationDestination, resolution destinationResolution) (offlineDestination, bool) {
	if resolution != destinationUnresolved {
		return offlineDestination{}, false
	}
	name := strings.TrimSpace(dest.Name)
	server := NormalizeClusterServer(dest.Server)
	if (name == "") == (server == "") {
		return offlineDestination{}, false
	}
	return offlineDestination{name: name, server: server}, true
}

// offlineDestinationPermitted evaluates Argo CD AppProject.isDestinationMatched
// (pkg/apis/application/v1alpha1/app_project_types.go, v3.6.1) over a
// destination whose name or server is unknown. Every entry is evaluated in
// three-valued logic: tristateTrue and tristateFalse are what Argo CD decides
// for every cluster the unknown field could name, and tristateUnknown means
// the decision depends on it. Upstream returns false as soon as an entry hits
// a deny case and otherwise reports whether any entry matched, so the result
// does not depend on entry order.
//
// The unresolved cluster is a live cluster Secret drydock cannot see, so its
// project scoping is unknown too. When it is scoped to proj, util/argo
// GetAppProjectByName adds an implicit destination for its server, which can
// make an entry match but never denies. Only a deny is therefore definite
// when no entry of proj matches.
func offlineDestinationPermitted(proj argoappv1.AppProject, dest offlineDestination, namespace string) tristate {
	anyMatched, denied := tristateFalse, tristateFalse
	for _, item := range proj.Spec.Destinations {
		nameMatched := offlineGlobMatch(item.Name, dest.name)
		serverMatched := offlineGlobMatch(item.Server, dest.server)
		namespaceMatched := tristateOf(argoDestinationGlobMatch(item.Namespace, namespace))

		matched := serverMatched.or(nameMatched).and(namespaceMatched)
		// v3.6.1 (d9e7e38c6): a name or server deny pattern denies only
		// when the entry's namespace also matches.
		nameOrServerDenied := nameMatched.not().and(tristateOf(isDestinationDenyPattern(item.Name))).
			or(serverMatched.not().and(tristateOf(isDestinationDenyPattern(item.Server)))).
			and(namespaceMatched)
		namespaceDenied := namespaceMatched.not().and(tristateOf(isDestinationDenyPattern(item.Namespace))).and(serverMatched)

		anyMatched = anyMatched.or(matched)
		denied = denied.or(matched.not().and(nameOrServerDenied.or(namespaceDenied)))
	}
	return denied.not().and(anyMatched.or(tristateUnknown))
}

// offlineGlobMatch is Argo CD's dstNameMatched or dstServerMatched for one
// entry pattern. A known value is matched exactly; an unknown one is a
// non-empty name or server the pattern decides only when it is `*`, empty,
// or the negation of either.
func offlineGlobMatch(pattern, value string) tristate {
	if value != "" {
		return tristateOf(argoDestinationGlobMatch(pattern, value))
	}
	if isDestinationDenyPattern(pattern) {
		return unknownValueGlobMatch(pattern[1:]).not()
	}
	return unknownValueGlobMatch(pattern)
}

func unknownValueGlobMatch(pattern string) tristate {
	switch pattern {
	case "*":
		return tristateTrue
	case "":
		return tristateFalse
	default:
		return tristateUnknown
	}
}

// argoDestinationGlobMatch is Argo CD's globMatch with negation allowed and
// no separators, as isDestinationMatched calls it.
func argoDestinationGlobMatch(pattern, value string) bool {
	if isDestinationDenyPattern(pattern) {
		return !argoglob.Match(pattern[1:], value)
	}
	if pattern == "*" {
		return true
	}
	return argoglob.Match(pattern, value)
}

func isDestinationDenyPattern(pattern string) bool {
	return strings.HasPrefix(pattern, "!")
}

// unresolvedDestinationDeferredMessage reports a destination whose AppProject
// decision depends on the cluster name or server drydock cannot resolve
// offline. Both messages classify as deferred project diagnostics.
func unresolvedDestinationDeferredMessage(app argoappv1.Application, dest offlineDestination) string {
	if dest.name != "" {
		return fmt.Sprintf("Application %s destination name %q cannot be resolved against AppProject server policy offline", applicationName(app), dest.name)
	}
	return fmt.Sprintf("Application %s destination server %q has no cluster Secret; its cluster name cannot be resolved against AppProject name policy offline", applicationName(app), remote.RedactURL(dest.server))
}
