package app

import (
	"reflect"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
)

func TestPolicyPluginBuildEnvMirrorsRepoServerOrder(t *testing.T) {
	opts := render.RenderOptions{
		ArgoEnv: argoappv1.Env{
			{Name: "ARGOCD_APP_NAME", Value: "demo"},
			{Name: "ARGOCD_APP_NAMESPACE", Value: ""},
		},
		KubeVersion: "1.30.2",
		APIVersions: []string{"apps/v1", "monitoring.coreos.com/v1"},
	}
	got := policyPluginBuildEnv(opts)
	want := []string{
		"ARGOCD_APP_NAME=demo",
		"ARGOCD_APP_NAMESPACE=",
		"KUBE_VERSION=1.30.2",
		"KUBE_API_VERSIONS=apps/v1,monitoring.coreos.com/v1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policyPluginBuildEnv() = %#v, want %#v", got, want)
	}
}

func TestPolicyPluginBuildEnvKeepsKubeKeysWhenUnset(t *testing.T) {
	got := policyPluginBuildEnv(render.RenderOptions{})
	want := []string{"KUBE_VERSION=", "KUBE_API_VERSIONS="}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policyPluginBuildEnv() with no kube inputs = %#v, want key-present empty values like Argo CD", got)
	}
}

func TestRecordApplicationPluginParameterEnvEmitsNullWithoutParameters(t *testing.T) {
	for _, params := range []argoappv1.ApplicationSourcePluginParameters{nil, {}} {
		out := newValidatedPluginParameters()
		if msg := recordApplicationPluginParameterEnv("p", params, &out); msg != "" {
			t.Fatal(msg)
		}
		if want := []string{"ARGOCD_APP_PARAMETERS=null"}; !reflect.DeepEqual(out.extraEnv, want) {
			t.Fatalf("extraEnv for %#v = %#v, want %#v", params, out.extraEnv, want)
		}
	}
}

func TestComposePolicyPluginExtraEnvOrdersBuildEnvParametersThenExtras(t *testing.T) {
	opts := render.RenderOptions{ArgoEnv: argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}}}
	params := validatedPluginParameters{extraEnv: []string{"ARGOCD_APP_PARAMETERS=null"}}
	got := composePolicyPluginExtraEnv(opts, params, true)
	want := []string{
		"ARGOCD_APP_NAME=demo",
		"KUBE_VERSION=",
		"KUBE_API_VERSIONS=",
		"ARGOCD_APP_PARAMETERS=null",
		"DRYDOCK_OFFLINE=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("composePolicyPluginExtraEnv() = %#v, want %#v", got, want)
	}
	if got := composePolicyPluginExtraEnv(opts, params, false); got[len(got)-1] != "ARGOCD_APP_PARAMETERS=null" {
		t.Fatalf("composePolicyPluginExtraEnv(offline=false) = %#v, want no DRYDOCK_OFFLINE entry", got)
	}
}
