package app

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func TestValidatePolicyPluginSourceMessages(t *testing.T) {
	source := render.ResolvedSource{Path: "apps/demo"}
	value := "prod"
	env := argoappv1.Env{{Name: "MODE", Value: "secret-value"}, {Name: "SUFFIX", Value: "x"}, nil}
	params := argoappv1.ApplicationSourcePluginParameters{{Name: "values", String_: &value}}
	testCases := []struct {
		name     string
		engine   pluginpolicy.Engine
		plugin   render.PluginConfig
		wantSubs []string
		wantNot  []string
	}{
		{name: "env on exec is validated later", engine: pluginpolicy.EngineExec, plugin: render.PluginConfig{Name: "pkl", Env: env}},
		{name: "env on container is validated later", engine: pluginpolicy.EngineContainer, plugin: render.PluginConfig{Name: "pkl", Env: env}},
		{name: "env on native engine", engine: pluginpolicy.EngineAVPCompat, plugin: render.PluginConfig{Name: "avp", Env: env}, wantSubs: []string{"Application plugin env", "trusted native plugin policy"}, wantNot: []string{"parameters.allow", "secret-value"}},
		{name: "parameters on native engine", engine: pluginpolicy.EngineNativeKustomize, plugin: render.PluginConfig{Name: "k", Parameters: params}, wantSubs: []string{"Application plugin parameters", "trusted native plugin policy"}},
		{name: "parameters on exec are fine here", engine: pluginpolicy.EngineExec, plugin: render.PluginConfig{Name: "pkl", Parameters: params}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			plugin := testCase.plugin
			got := validatePolicyPluginSource(plugin.Name, source, render.RenderOptions{Plugin: &plugin}, pluginpolicy.Plugin{Engine: testCase.engine})
			if len(testCase.wantSubs) == 0 {
				if got != "" {
					t.Fatalf("message = %q, want empty", got)
				}
				return
			}
			for _, sub := range testCase.wantSubs {
				if !strings.Contains(got, sub) {
					t.Fatalf("message = %q, want substring %q", got, sub)
				}
			}
			for _, sub := range testCase.wantNot {
				if strings.Contains(got, sub) {
					t.Fatalf("message = %q, must not contain %q", got, sub)
				}
			}
		})
	}
}
