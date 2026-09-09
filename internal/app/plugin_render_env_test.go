package app

import (
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pluginexec"
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
	applicationEnv := []string{"ARGOCD_ENV_MODE=prod"}
	got := composePolicyPluginExtraEnv(opts, applicationEnv, params, true)
	want := []string{
		"ARGOCD_APP_NAME=demo",
		"KUBE_VERSION=",
		"KUBE_API_VERSIONS=",
		"ARGOCD_ENV_MODE=prod",
		"ARGOCD_APP_PARAMETERS=null",
		"DRYDOCK_OFFLINE=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("composePolicyPluginExtraEnv() = %#v, want %#v (build env, ARGOCD_ENV_*, parameters, extras)", got, want)
	}
	if got := composePolicyPluginExtraEnv(opts, applicationEnv, params, false); got[len(got)-1] != "ARGOCD_APP_PARAMETERS=null" {
		t.Fatalf("composePolicyPluginExtraEnv(offline=false) = %#v, want no DRYDOCK_OFFLINE entry", got)
	}
	withoutApplicationEnv := []string{
		"ARGOCD_APP_NAME=demo",
		"KUBE_VERSION=",
		"KUBE_API_VERSIONS=",
		"ARGOCD_APP_PARAMETERS=null",
		"DRYDOCK_OFFLINE=true",
	}
	if got := composePolicyPluginExtraEnv(opts, nil, params, true); !reflect.DeepEqual(got, withoutApplicationEnv) {
		t.Fatalf("composePolicyPluginExtraEnv(nil application env) = %#v, want %#v (no ARGOCD_ENV_* entry)", got, withoutApplicationEnv)
	}
}

func TestValidateApplicationPluginEnvDeliversArgoEnvWithBuildEnvSubstitution(t *testing.T) {
	build := argoappv1.Env{
		{Name: "ARGOCD_APP_NAME", Value: "demo"},
		{Name: "KUBE_VERSION", Value: "1.30.2"},
	}
	env := argoappv1.Env{
		{Name: "MODE", Value: "prod"},
		// MODE precedes SUFFIX, so $MODE and $ARGOCD_ENV_MODE prove that earlier
		// Application env entries are not in the substitution scope.
		{Name: "SUFFIX", Value: "$ARGOCD_APP_NAME-$KUBE_VERSION-$$-$UNKNOWN-$CLUSTER_NAME-$MODE-$ARGOCD_ENV_MODE"},
		{Name: "EMPTY", Value: ""},
	}
	got, message := validateApplicationPluginEnv("pkl", []string{"EMPTY", "MODE", "SUFFIX"}, env, build)
	if message != "" {
		t.Fatalf("message = %q, want empty", message)
	}
	want := []string{
		"ARGOCD_ENV_MODE=prod",
		"ARGOCD_ENV_SUFFIX=demo-1.30.2-$----", // $$ -> $; unknown, host, and earlier Application env names expand to empty
		"ARGOCD_ENV_EMPTY=",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validateApplicationPluginEnv() = %#v, want %#v (spec order, build-env substitution only)", got, want)
	}
}

func TestValidateApplicationPluginEnvFailsClosed(t *testing.T) {
	build := argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}}
	// Every value carries the sentinel so any message shape that echoes a
	// value is caught; the oversized value is additionally a run of one byte.
	const sentinel = "sentinel-value"
	big := sentinel + strings.Repeat("x", pluginexec.MaxEnvValueBytes)
	testCases := []struct {
		name  string
		allow []string
		env   argoappv1.Env
		want  string
	}{
		{name: "not allowlisted", allow: []string{"MODE"}, env: argoappv1.Env{{Name: "SECRET", Value: sentinel}}, want: `Application plugin env "SECRET", which is not allowed by policy applicationEnv.allow`},
		{name: "no allowlist at all", allow: nil, env: argoappv1.Env{{Name: "MODE", Value: sentinel}}, want: "applicationEnv.allow"},
		{name: "invalid name", allow: []string{"MODE"}, env: argoappv1.Env{{Name: "9MODE", Value: sentinel}}, want: "invalid Application plugin env name"},
		{name: "nil entry", allow: []string{"MODE"}, env: argoappv1.Env{nil}, want: "unnamed Application plugin env entry"},
		{name: "blank name", allow: []string{"MODE"}, env: argoappv1.Env{{Name: " ", Value: sentinel}}, want: "unnamed Application plugin env entry"},
		{name: "duplicate", allow: []string{"MODE"}, env: argoappv1.Env{{Name: "MODE", Value: sentinel + "-a"}, {Name: "MODE", Value: sentinel + "-b"}}, want: `duplicate Application plugin env "MODE"`},
		{name: "too large", allow: []string{"MODE"}, env: argoappv1.Env{{Name: "MODE", Value: big}}, want: `Application plugin env "MODE" value is too large`},
		{name: "NUL byte", allow: []string{"MODE"}, env: argoappv1.Env{{Name: "MODE", Value: sentinel + "\x00" + sentinel}}, want: `Application plugin env "MODE" value contains a NUL byte`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, message := validateApplicationPluginEnv("pkl", testCase.allow, testCase.env, build)
			if got != nil {
				t.Fatalf("entries = %#v, want none on failure", got)
			}
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("message = %q, want substring %q", message, testCase.want)
			}
			if strings.Contains(message, sentinel) || strings.Contains(message, "xxxx") {
				t.Fatalf("message = %q leaks a value", message)
			}
		})
	}
	if got, message := validateApplicationPluginEnv("pkl", nil, nil, build); message != "" || got != nil {
		t.Fatalf("no env, no allowlist: got %#v / %q, want nil / empty", got, message)
	}
}

func TestPolicyPluginBuildEnvEntriesMatchEnviron(t *testing.T) {
	opts := render.RenderOptions{
		// A nil element is legal in the public RenderOptions.ArgoEnv and must be
		// dropped: Env.Envsubst dereferences every entry.
		ArgoEnv:     argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}, nil},
		KubeVersion: "1.30.2",
		APIVersions: []string{"apps/v1"},
	}
	entries := policyPluginBuildEnvEntries(opts)
	want := []string{"ARGOCD_APP_NAME=demo", "KUBE_VERSION=1.30.2", "KUBE_API_VERSIONS=apps/v1"}
	if got := entries.Environ(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ() = %#v, want %#v", got, want)
	}
	if got := policyPluginBuildEnv(opts); !reflect.DeepEqual(got, want) {
		t.Fatalf("policyPluginBuildEnv() = %#v, want %#v", got, want)
	}
	if got := entries.Envsubst("$KUBE_VERSION/$KUBE_API_VERSIONS"); got != "1.30.2/apps/v1" {
		t.Fatalf("Envsubst = %q", got)
	}
}
