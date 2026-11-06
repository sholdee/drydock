package app

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/sync"
	kubeutil "github.com/argoproj/argo-cd/gitops-engine/v3/pkg/utils/kube"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// namespacedInfoProvider says that every kind is namespaced, which holds for
// the ConfigMaps and ServiceAccounts in these tests.
type namespacedInfoProvider struct{}

func (namespacedInfoProvider) IsNamespaced(schema.GroupKind) (bool, error) { return true, nil }

// upstreamNormalizeTargetObjects is the repeat handling of
// NormalizeTargetObjects (Argo CD v3.6.1 controller/state.go:575-623) for
// namespaced objects: an object without a namespace gets the destination
// namespace, an object with a generateName and no name gets a key of its
// own, and the last copy of each key is kept. The controller package is not
// in drydock's module graph, so this is a copy. The tracking metadata is
// left out, because the tests compare names and data only.
func upstreamNormalizeTargetObjects(namespace string, objs []*unstructured.Unstructured) []*unstructured.Unstructured {
	targetByKey := make(map[kubeutil.ResourceKey][]*unstructured.Unstructured)
	var keys []kubeutil.ResourceKey
	for i := range objs {
		obj := objs[i]
		if obj.GetNamespace() == "" {
			obj.SetNamespace(namespace)
		}
		key := kubeutil.GetResourceKey(obj)
		if key.Name == "" && obj.GetGenerateName() != "" {
			key.Name = fmt.Sprintf("%s%d", obj.GetGenerateName(), i)
		}
		if _, ok := targetByKey[key]; !ok {
			keys = append(keys, key)
		}
		targetByKey[key] = append(targetByKey[key], obj)
	}
	result := make([]*unstructured.Unstructured, 0, len(keys))
	for _, key := range keys {
		targets := targetByKey[key]
		result = append(result, targets[len(targets)-1])
	}
	return result
}

// upstreamManagedAndHooks is what the Argo CD v3.6.1 controller does with the
// target objects of an Application: NormalizeTargetObjects, then the
// gitops-engine Reconcile itself, which splits the hooks off.
func upstreamManagedAndHooks(namespace string, objs []*unstructured.Unstructured) (managed, hooks []string) {
	normalized := upstreamNormalizeTargetObjects(namespace, objs)
	reconciled := sync.Reconcile(normalized, map[kubeutil.ResourceKey]*unstructured.Unstructured{}, namespace, namespacedInfoProvider{})
	return objectLabels(reconciled.Target), objectLabels(reconciled.Hooks)
}

func objectLabels(objs []*unstructured.Unstructured) []string {
	out := []string{}
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		value, _, _ := unstructured.NestedString(obj.Object, "data", "value")
		name := obj.GetName()
		if name == "" {
			name = obj.GetGenerateName() + "*"
		}
		out = append(out, obj.GetKind()+"/"+name+"="+value)
	}
	sort.Strings(out)
	return out
}

func manifestLabels(manifests []render.Manifest) []string {
	objs := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		objs = append(objs, manifest.Object)
	}
	return objectLabels(objs)
}

func withHook(obj *unstructured.Unstructured, annotations map[string]string) *unstructured.Unstructured {
	obj.SetAnnotations(annotations)
	return obj
}

func generateNamed(obj *unstructured.Unstructured, prefix string) *unstructured.Unstructured {
	unstructured.RemoveNestedField(obj.Object, "metadata", "name")
	obj.SetGenerateName(prefix)
	return obj
}

// TestRenderApplicationRepeatedHooksMatchUpstream renders sources that hold
// a resource more than once, as a hook and as a regular resource. The Argo
// CD controller keeps the last copy among all target objects before it
// splits the hooks off, so the kind of the last copy decides whether Argo CD
// manages the resource. drydock must keep the same copies in Manifests and
// Hooks.
func TestRenderApplicationRepeatedHooksMatchUpstream(t *testing.T) {
	preInstall := map[string]string{"helm.sh/hook": "pre-install"}
	syncHook := map[string]string{"argocd.argoproj.io/hook": "Sync"}
	for _, tt := range []struct {
		name    string
		sources [][]*unstructured.Unstructured
	}{
		{
			// helm prints hooks last, so a ServiceAccount that a chart
			// renders as a template and as a pre-install hook is a hook.
			name: "hook copy after a regular copy",
			sources: [][]*unstructured.Unstructured{{
				cm("sa", "regular"),
				withHook(cm("sa", "hook"), preInstall),
				cm("other", "kept"),
			}},
		},
		{
			name: "regular copy after a hook copy",
			sources: [][]*unstructured.Unstructured{{
				withHook(cm("sa", "hook"), preInstall),
				cm("sa", "regular"),
			}},
		},
		{
			name: "two hook copies",
			sources: [][]*unstructured.Unstructured{{
				withHook(cm("job", "first"), syncHook),
				withHook(cm("job", "second"), syncHook),
			}},
		},
		{
			name: "hook copy in a later source",
			sources: [][]*unstructured.Unstructured{
				{cm("a", "regular-a"), cm("b", "regular-b"), cm("c", "regular-c")},
				{withHook(cm("a", "hook-a"), syncHook), cm("c", "second-c")},
			},
		},
		{
			name: "regular copy moves indexes after a removed hook",
			sources: [][]*unstructured.Unstructured{{
				withHook(cm("x", "hook-x"), syncHook),
				withHook(cm("y", "hook-y"), syncHook),
				cm("x", "regular-x"),
				withHook(cm("y", "hook-y2"), syncHook),
			}},
		},
		{
			name: "generateName objects are never repeats",
			sources: [][]*unstructured.Unstructured{{
				withHook(generateNamed(cm("x", "first"), "job-"), syncHook),
				withHook(generateNamed(cm("x", "second"), "job-"), syncHook),
				generateNamed(cm("x", "third"), "plain-"),
				generateNamed(cm("x", "fourth"), "plain-"),
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			renderers := StaticRenderers{}
			application := argoappv1.Application{Name: "demo"}
			application.Spec.Destination.Namespace = "default"
			var all []*unstructured.Unstructured
			for i, objs := range tt.sources {
				path := fmt.Sprintf("source-%d", i)
				application.Spec.Sources = append(application.Spec.Sources, argoappv1.ApplicationSource{RepoURL: "https://repo", Path: path})
				for _, obj := range objs {
					renderers[path] = append(renderers[path], render.Manifest{Object: obj.DeepCopy()})
					all = append(all, obj.DeepCopy())
				}
			}
			result, err := RenderApplication(context.Background(), application, renderers)
			if err != nil {
				t.Fatalf("RenderApplication() error = %v", err)
			}
			wantManaged, wantHooks := upstreamManagedAndHooks("default", all)
			if got := manifestLabels(result.Manifests); !reflect.DeepEqual(got, wantManaged) {
				t.Fatalf("Manifests = %v, upstream managed %v", got, wantManaged)
			}
			if got := manifestLabels(result.Hooks); !reflect.DeepEqual(got, wantHooks) {
				t.Fatalf("Hooks = %v, upstream hooks %v", got, wantHooks)
			}
		})
	}
}

// TestRenderApplicationRepeatedResourceMessage pins the repeated-resource
// warning. It must hold when both copies come from one source and when one
// copy is a hook, so it does not name a source.
func TestRenderApplicationRepeatedResourceMessage(t *testing.T) {
	application := argoappv1.Application{Name: "demo"}
	application.Spec.Destination.Namespace = "default"
	application.Spec.Source = &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "chart"}
	renderers := StaticRenderers{"chart": {
		{Object: cm("sa", "regular")},
		{Object: withHook(cm("sa", "hook"), map[string]string{"helm.sh/hook": "pre-install"})},
	}}
	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	const want = "resource ConfigMap default/sa appears more than once in Application demo; drydock keeps the last copy, as Argo CD does"
	for _, diag := range result.Diagnostics {
		if diag.Category == "repeated-resource" && diag.Message == want {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want %q", result.Diagnostics, want)
}
