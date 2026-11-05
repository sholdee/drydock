package config

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// upstreamResourceOverrides loads argocd-cm data through Argo CD's own
// SettingsManager.GetResourceOverrides (util/settings/settings.go at v3.6.1),
// backed by a fake clientset. resource.compareoptions is set to
// ignoreResourceStatusField: none, so Argo CD does not add its ignore-status
// override and the map holds only what resource.customizations builds.
func upstreamResourceOverrides(t *testing.T, data map[string]string) (overrides map[string]argoappv1.ResourceOverride, err error, panicked bool) {
	t.Helper()
	cmData := map[string]string{"resource.compareoptions": "ignoreResourceStatusField: none\n"}
	maps.Copy(cmData, data)
	cm := &corev1.ConfigMap{Data: cmData}
	cm.Name = "argocd-cm"
	cm.Namespace = "argocd"
	cm.Labels = map[string]string{"app.kubernetes.io/part-of": "argocd"}
	manager := argosettings.NewSettingsManager(t.Context(), fake.NewClientset(cm), "argocd")
	defer func() {
		if recovered := recover(); recovered != nil {
			panicked = true
		}
	}()
	overrides, err = manager.GetResourceOverrides()
	return overrides, err, false
}

// comparableOverride is the part of a resource override that drydock keeps.
// Health Lua and actions are compared without leading and trailing white
// space, because drydock treats white space only as unset.
type comparableOverride struct {
	IgnoreDifferences     [3][]string
	IgnoreResourceUpdates [3][]string
	KnownTypeFields       []string
	HealthLua             string
	UseOpenLibs           bool
	ActionsSHA256         string
}

func comparableIgnore(jsonPointers, jqPathExpressions, managedFieldsManagers []string) [3][]string {
	out := [3][]string{}
	for i, list := range [][]string{jsonPointers, jqPathExpressions, managedFieldsManagers} {
		if len(list) > 0 {
			out[i] = append([]string(nil), list...)
		}
	}
	return out
}

func comparableUpstreamOverrides(overrides map[string]argoappv1.ResourceOverride) map[string]comparableOverride {
	out := map[string]comparableOverride{}
	for key, override := range overrides {
		value := comparableOverride{
			IgnoreDifferences:     comparableIgnore(override.IgnoreDifferences.JSONPointers, override.IgnoreDifferences.JQPathExpressions, override.IgnoreDifferences.ManagedFieldsManagers),
			IgnoreResourceUpdates: comparableIgnore(override.IgnoreResourceUpdates.JSONPointers, override.IgnoreResourceUpdates.JQPathExpressions, override.IgnoreResourceUpdates.ManagedFieldsManagers),
			HealthLua:             strings.TrimSpace(override.HealthLua),
			UseOpenLibs:           override.UseOpenLibs,
		}
		for _, field := range override.KnownTypeFields {
			value.KnownTypeFields = append(value.KnownTypeFields, field.Field+"="+field.Type)
		}
		if strings.TrimSpace(override.Actions) != "" {
			value.ActionsSHA256 = stringFingerprint(override.Actions)
		}
		// An empty override changes no diff or health result, so drydock
		// does not keep it.
		if reflect.DeepEqual(value, comparableOverride{}) {
			continue
		}
		out[key] = value
	}
	return out
}

func comparableDrydockCustomizations(customizations map[string]ResourceCustomization) map[string]comparableOverride {
	out := map[string]comparableOverride{}
	for key, customization := range customizations {
		value := comparableOverride{
			IgnoreDifferences:     comparableIgnore(customization.IgnoreDifferences.JSONPointers, customization.IgnoreDifferences.JQPathExpressions, customization.IgnoreDifferences.ManagedFieldsManagers),
			IgnoreResourceUpdates: comparableIgnore(customization.IgnoreResourceUpdates.JSONPointers, customization.IgnoreResourceUpdates.JQPathExpressions, customization.IgnoreResourceUpdates.ManagedFieldsManagers),
			HealthLua:             strings.TrimSpace(customization.HealthLua),
			UseOpenLibs:           customization.UseOpenLibs,
			ActionsSHA256:         customization.Actions.fingerprint,
		}
		for _, field := range customization.KnownTypeFields {
			value.KnownTypeFields = append(value.KnownTypeFields, field.Field+"="+field.Type)
		}
		out[key] = value
	}
	return out
}

// TestResourceOverridesParityWithArgoCD loads the same argocd-cm data
// through Argo CD's SettingsManager.GetResourceOverrides and through
// drydock's provider. When Argo CD returns an error, drydock must report an
// error diagnostic and keep no resource customizations. Otherwise both must
// build the same override map.
func TestResourceOverridesParityWithArgoCD(t *testing.T) {
	const (
		ignoreA = "managedFieldsManagers: [a]\n"
		ignoreB = "managedFieldsManagers: [b]\n"
	)
	for _, tc := range []struct {
		name string
		data map[string]string
		// wantKey, when set, must be in the override map. It is checked so
		// that a case cannot pass because both sides build nothing.
		wantKey string
		wantErr bool
	}{
		// A split key and the block set the same field for the same key.
		{name: "split key replaces block core key", data: map[string]string{
			"resource.customizations":                            "/Service:\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences._Service": ignoreB,
		}, wantKey: "/Service"},
		{name: "split all replaces block star key", data: map[string]string{
			"resource.customizations":                       "'*/*':\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences.all": ignoreB,
		}, wantKey: "*/*"},
		{name: "split key replaces block kind key", data: map[string]string{
			"resource.customizations":                           "Service:\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences.Service": ignoreB,
		}, wantKey: "Service"},
		{name: "split key replaces block group key", data: map[string]string{
			"resource.customizations":                                   "apps/Deployment:\n  ignoreDifferences: |\n    jsonPointers: [/spec/replicas]\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences.apps_Deployment": "jqPathExpressions: [.spec.paused]\n",
		}, wantKey: "apps/Deployment"},
		{name: "split key keeps other block fields", data: map[string]string{
			"resource.customizations":                            "/Service:\n  health.lua: return {status=\"Healthy\"}\n  health.lua.useOpenLibs: true\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences._Service": ignoreB,
		}, wantKey: "/Service"},
		{name: "split health replaces block health", data: map[string]string{
			"resource.customizations":                        "apps/Deployment:\n  health.lua: return {status=\"Healthy\"}\n",
			"resource.customizations.health.apps_Deployment": "return {status=\"Progressing\"}\n",
		}, wantKey: "apps/Deployment"},
		{name: "split useOpenLibs false replaces block true", data: map[string]string{
			"resource.customizations":                             "apps/Deployment:\n  health.lua: return {status=\"Healthy\"}\n  health.lua.useOpenLibs: true\n",
			"resource.customizations.useOpenLibs.apps_Deployment": "false",
		}, wantKey: "apps/Deployment"},
		{name: "split actions replace block actions", data: map[string]string{
			"resource.customizations":                         "apps/Deployment:\n  actions: |\n    definitions:\n    - name: a\n      action.lua: return obj\n",
			"resource.customizations.actions.apps_Deployment": "definitions:\n- name: b\n  action.lua: return obj\n",
		}, wantKey: "apps/Deployment"},
		{name: "split knownTypeFields replace block", data: map[string]string{
			"resource.customizations":                                     "argoproj.io/Rollout:\n  knownTypeFields:\n  - field: spec.a\n    type: core/v1/PodSpec\n",
			"resource.customizations.knownTypeFields.argoproj.io_Rollout": "- field: spec.b\n  type: core/v1/PodSpec\n",
		}, wantKey: "argoproj.io/Rollout"},
		{name: "split ignoreResourceUpdates replace block", data: map[string]string{
			"resource.customizations": "apps/Deployment:\n  ignoreResourceUpdates: |\n    jsonPointers: [/a]\n",
			"resource.customizations.ignoreResourceUpdates.apps_Deployment": "jsonPointers: [/b]\n",
		}, wantKey: "apps/Deployment"},
		{name: "split keys of several types share a key", data: map[string]string{
			"resource.customizations.health._Service":            "return {status=\"Healthy\"}\n",
			"resource.customizations.ignoreDifferences._Service": ignoreA,
			"resource.customizations.useOpenLibs._Service":       "true",
		}, wantKey: "/Service"},
		{name: "block all key is not star", data: map[string]string{
			"resource.customizations": "all:\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
		}, wantKey: "all"},
		{name: "block field names match in any case", data: map[string]string{
			"resource.customizations": "/Service:\n  IgnoreDifferences: |\n    ManagedFieldsManagers: [a]\n",
		}, wantKey: "/Service"},

		// Empty split values replace the field too.
		{name: "empty split ignoreDifferences clears block", data: map[string]string{
			"resource.customizations":                            "/Service:\n  health.lua: return {status=\"Healthy\"}\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences._Service": "",
		}, wantKey: "/Service"},
		{name: "empty split health clears block", data: map[string]string{
			"resource.customizations":                 "/Service:\n  health.lua: return {status=\"Healthy\"}\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.health._Service": "",
		}, wantKey: "/Service"},
		{name: "empty split useOpenLibs fails", data: map[string]string{
			"resource.customizations.useOpenLibs._Service": "",
		}, wantErr: true},
		{name: "useOpenLibs with newline fails", data: map[string]string{
			"resource.customizations.useOpenLibs._Service": "true\n",
		}, wantErr: true},

		// Split key suffix forms.
		{name: "trailing underscore", data: map[string]string{
			"resource.customizations.ignoreDifferences.Service_": ignoreA,
		}, wantKey: "Service/"},
		{name: "lone underscore", data: map[string]string{
			"resource.customizations.ignoreDifferences._": ignoreA,
		}, wantKey: "/"},
		{name: "core group", data: map[string]string{
			"resource.customizations.ignoreDifferences._Service": ignoreA,
		}, wantKey: "/Service"},
		{name: "bare kind", data: map[string]string{
			"resource.customizations.ignoreDifferences.Service": ignoreA,
		}, wantKey: "Service"},
		{name: "dotted group", data: map[string]string{
			"resource.customizations.ignoreDifferences.cert-manager.io_Certificate": ignoreA,
		}, wantKey: "cert-manager.io/Certificate"},
		{name: "dotted suffix without underscore", data: map[string]string{
			"resource.customizations.ignoreDifferences.apps.Deployment": ignoreA,
		}, wantKey: "apps.Deployment"},
		{name: "all", data: map[string]string{
			"resource.customizations.ignoreDifferences.all": ignoreA,
		}, wantKey: "*/*"},
		{name: "underscore all", data: map[string]string{
			"resource.customizations.ignoreDifferences._all": ignoreA,
		}, wantKey: "/all"},
		{name: "capital All", data: map[string]string{
			"resource.customizations.ignoreDifferences.All": ignoreA,
		}, wantKey: "All"},
		{name: "empty suffix fails", data: map[string]string{
			"resource.customizations.ignoreDifferences.": ignoreA,
		}, wantErr: true},
		{name: "two underscores fail", data: map[string]string{
			"resource.customizations.ignoreDifferences.apps_Deployment_extra": ignoreA,
		}, wantErr: true},
		{name: "double underscore fails", data: map[string]string{
			"resource.customizations.ignoreDifferences.__": ignoreA,
		}, wantErr: true},
		{name: "invalid key fails whole map", data: map[string]string{
			"resource.customizations":                         "/Service:\n  ignoreDifferences: |\n    managedFieldsManagers: [a]\n",
			"resource.customizations.ignoreDifferences.a_b_c": ignoreA,
		}, wantErr: true},
		{name: "type only key is ignored", data: map[string]string{
			"resource.customizations.health":                     "return {}\n",
			"resource.customizations.ignoreDifferences._Service": ignoreA,
		}, wantKey: "/Service"},
		{name: "unknown type fails", data: map[string]string{
			"resource.customizations.foo._Service": ignoreA,
		}, wantErr: true},
		{name: "dots after the type stay in the key", data: map[string]string{
			"resource.customizations.health.lua.useOpenLibs._Service": "return {status=\"Healthy\"}\n",
		}, wantKey: "lua.useOpenLibs./Service"},
		{name: "prefix without dot is a split key", data: map[string]string{
			"resource.customizationsX.ignoreDifferences._Service": ignoreA,
		}, wantKey: "/Service"},

		// Values Argo CD cannot decode.
		{name: "invalid split ignoreDifferences fails", data: map[string]string{
			"resource.customizations.ignoreDifferences._Service": "jsonPointers: [\n",
		}, wantErr: true},
		{name: "invalid split ignoreResourceUpdates fails", data: map[string]string{
			"resource.customizations.ignoreResourceUpdates._Service": "jsonPointers: [\n",
		}, wantErr: true},
		{name: "invalid split knownTypeFields fails", data: map[string]string{
			"resource.customizations.knownTypeFields._Service": "field: a\n",
		}, wantErr: true},
		{name: "invalid block fails whole map", data: map[string]string{
			"resource.customizations":                           "/Service: [\n",
			"resource.customizations.ignoreDifferences.Service": ignoreA,
		}, wantErr: true},
		{name: "invalid block ignoreDifferences fails", data: map[string]string{
			"resource.customizations": "/Service:\n  ignoreDifferences: |\n    jsonPointers: [\n",
		}, wantErr: true},
		{name: "block ignoreDifferences as a map fails", data: map[string]string{
			"resource.customizations": "/Service:\n  ignoreDifferences:\n    jsonPointers: [/a]\n",
		}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, upstreamErr, panicked := upstreamResourceOverrides(t, tc.data)
			if panicked {
				t.Fatalf("Argo CD panicked; add the case as a drydock-only test")
			}
			if (upstreamErr != nil) != tc.wantErr {
				t.Fatalf("Argo CD error = %v, wantErr %v", upstreamErr, tc.wantErr)
			}

			settings := DefaultSettings()
			diags := applyCMMap(&settings, tc.data, "argocd-cm.yaml", "data")
			if upstreamErr != nil {
				if !hasErrorDiagnostic(diags) {
					t.Fatalf("Argo CD error = %v; drydock diagnostics = %#v, want an error", upstreamErr, diags)
				}
				if len(settings.ResourceCustomizations) != 0 {
					t.Fatalf("Argo CD error = %v; drydock kept %#v, want none", upstreamErr, settings.ResourceCustomizations)
				}
				return
			}
			if hasErrorDiagnostic(diags) {
				t.Fatalf("Argo CD accepted the data; drydock diagnostics = %#v", diags)
			}
			want := comparableUpstreamOverrides(upstream)
			got := comparableDrydockCustomizations(settings.ResourceCustomizations)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("override map mismatch\ndrydock: %#v\nArgo CD: %#v", got, want)
			}
			if tc.wantKey != "" {
				if _, ok := want[tc.wantKey]; !ok {
					t.Fatalf("Argo CD map %#v has no key %q", want, tc.wantKey)
				}
			}
		})
	}
}

// TestResourceOverridesBlockNullEntryFails covers a block entry with no
// value. Argo CD's ResourceOverride.UnmarshalJSON panics on it, so Argo CD
// cannot load the overrides. drydock reports an error and keeps none.
func TestResourceOverridesBlockNullEntryFails(t *testing.T) {
	for _, value := range []string{"/Service:\n", "/Service: ~\n", "/Service: null\napps/Deployment:\n  health.lua: return {}\n"} {
		data := map[string]string{"resource.customizations": value}
		if _, _, panicked := upstreamResourceOverrides(t, data); !panicked {
			t.Fatalf("Argo CD did not panic on %q; move the case to the parity table", value)
		}
		settings := DefaultSettings()
		diags := applyCMMap(&settings, data, "argocd-cm.yaml", "data")
		if !hasErrorDiagnostic(diags) {
			t.Fatalf("%q: diagnostics = %#v, want an error", value, diags)
		}
		if len(settings.ResourceCustomizations) != 0 {
			t.Fatalf("%q: ResourceCustomizations = %#v, want none", value, settings.ResourceCustomizations)
		}
	}
}

// TestResourceOverridesSplitKeyOrderIsSorted covers the one case where Argo
// CD's result depends on Go map order: two split keys that set the same field
// for the same override key. With valid ConfigMap keys this needs Argo CD's
// prefix check without a trailing dot. drydock keeps the last key in sorted
// order and warns.
func TestResourceOverridesSplitKeyOrderIsSorted(t *testing.T) {
	data := map[string]string{
		"resource.customizations.ignoreDifferences._Service":  "managedFieldsManagers: [first]\n",
		"resource.customizationsX.ignoreDifferences._Service": "managedFieldsManagers: [last]\n",
	}
	settings := DefaultSettings()
	diags := applyCMMap(&settings, data, "argocd-cm.yaml", "data")
	if hasErrorDiagnostic(diags) {
		t.Fatalf("diagnostics = %#v, want no error", diags)
	}
	got := settings.ResourceCustomizations["/Service"].IgnoreDifferences.ManagedFieldsManagers
	if !reflect.DeepEqual(got, []string{"last"}) {
		t.Fatalf("managedFieldsManagers = %v, want [last]", got)
	}
	warned := false
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityWarning && strings.Contains(diag.Message, "random order") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("diagnostics = %#v, want a random-order warning", diags)
	}

	// Argo CD gives one of the two values, depending on map order.
	upstream, err, panicked := upstreamResourceOverrides(t, data)
	if err != nil || panicked {
		t.Fatalf("Argo CD error = %v, panicked = %v", err, panicked)
	}
	upstreamManagers := upstream["/Service"].IgnoreDifferences.ManagedFieldsManagers
	if !reflect.DeepEqual(upstreamManagers, []string{"first"}) && !reflect.DeepEqual(upstreamManagers, []string{"last"}) {
		t.Fatalf("Argo CD managedFieldsManagers = %v, want [first] or [last]", upstreamManagers)
	}
}
