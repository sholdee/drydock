package project

import (
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestOfflineDestinationPermittedIsSoundAgainstArgoCD enumerates small
// AppProjects and checks that every definite offline verdict is the one Argo
// CD's IsDestinationPermitted reaches for every value the unknown cluster
// name or server could take, and whether or not that unseen cluster is
// scoped to the project (util/argo GetAppProjectByName implicit
// destinations).
func TestOfflineDestinationPermittedIsSoundAgainstArgoCD(t *testing.T) {
	patterns := []string{"", "*", "!*", "a", "!a", "b", "!b", "a*"}
	namespaces := []string{"*", "x", "!x"}
	values := []string{"a", "b", "c"}

	entries := make([]argoappv1.ApplicationDestination, 0, len(patterns)*len(patterns)*len(namespaces))
	for _, name := range patterns {
		for _, server := range patterns {
			for _, namespace := range namespaces {
				entries = append(entries, argoappv1.ApplicationDestination{Name: name, Server: server, Namespace: namespace})
			}
		}
	}

	definite := 0
	check := func(destinations []argoappv1.ApplicationDestination) {
		definite += assertOfflineVerdictsSound(t, destinations, values)
	}
	for i, first := range entries {
		check([]argoappv1.ApplicationDestination{first})
		for _, second := range entries[i:] {
			check([]argoappv1.ApplicationDestination{first, second})
		}
	}
	if definite == 0 {
		t.Fatal("no definite offline verdicts were checked")
	}
}

// assertOfflineVerdictsSound checks every definite offline verdict for
// destinations against Argo CD over values and returns how many it checked.
func assertOfflineVerdictsSound(t *testing.T, destinations []argoappv1.ApplicationDestination, values []string) int {
	t.Helper()
	proj := argoappv1.AppProject{Spec: argoappv1.AppProjectSpec{Destinations: destinations}}
	definite := 0
	for _, namespace := range []string{"x", "y"} {
		for _, known := range values {
			for _, nameKnown := range []bool{true, false} {
				dest := offlineDestination{server: known}
				if nameKnown {
					dest = offlineDestination{name: known}
				}
				verdict := offlineDestinationPermitted(proj, dest, namespace)
				if verdict == tristateUnknown {
					continue
				}
				definite++
				for _, unknown := range values {
					cluster := &argoappv1.Cluster{Name: unknown, Server: known}
					if nameKnown {
						cluster = &argoappv1.Cluster{Name: known, Server: unknown}
					}
					// The unseen cluster is unscoped, or scoped to the
					// project for every namespace or for one namespace.
					for _, implicit := range [][]argoappv1.ApplicationDestination{
						nil,
						{{Server: cluster.Server, Namespace: "*"}},
						{{Server: cluster.Server, Namespace: "x"}},
					} {
						argoProj := proj.DeepCopy()
						argoProj.Spec.Destinations = append(argoProj.Spec.Destinations, implicit...)
						permitted, err := argoProj.IsDestinationPermitted(cluster, namespace, nil)
						if err != nil {
							t.Fatalf("IsDestinationPermitted() error = %v", err)
						}
						if permitted != (verdict == tristateTrue) {
							t.Fatalf("destinations %#v, offline %#v namespace %q: offline verdict %d, Argo CD permitted=%t for cluster %#v with implicit destinations %#v", destinations, dest, namespace, verdict, permitted, cluster, implicit)
						}
					}
				}
			}
		}
	}
	return definite
}

// TestOfflineDestinationDenialHoldsForUnnamedCluster checks every definite
// offline denial of a server-only destination against a cluster with no
// name. A cluster Secret can lack the name field (util/db SecretToCluster),
// and the v3.6.2 name veto of isDestinationMatched needs a cluster name.
func TestOfflineDestinationDenialHoldsForUnnamedCluster(t *testing.T) {
	patterns := []string{"", "*", "!*", "a", "!a"}
	denials := 0
	for _, name := range patterns {
		for _, server := range patterns {
			for _, entryNamespace := range []string{"*", "x", "!x"} {
				proj := argoappv1.AppProject{Spec: argoappv1.AppProjectSpec{Destinations: []argoappv1.ApplicationDestination{
					{Server: "*", Namespace: "*"},
					{Name: name, Server: server, Namespace: entryNamespace},
				}}}
				for _, namespace := range []string{"x", "y"} {
					if offlineDestinationPermitted(proj, offlineDestination{server: "a"}, namespace) != tristateFalse {
						continue
					}
					denials++
					permitted, err := proj.IsDestinationPermitted(&argoappv1.Cluster{Server: "a"}, namespace, nil)
					if err != nil {
						t.Fatalf("IsDestinationPermitted() error = %v", err)
					}
					if permitted {
						t.Fatalf("entry %#v namespace %q: offline denial, but Argo CD permits the cluster with no name", proj.Spec.Destinations[1], namespace)
					}
				}
			}
		}
	}
	if denials == 0 {
		t.Fatal("no definite offline denials were checked")
	}
}
