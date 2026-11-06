package config

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/manifestyaml"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// argoHelmConfigHelpers is the part of templates/_helpers.tpl in the
// argo-helm argo-cd chart 10.14.0 that builds the argocd-cm data, copied
// as it is.
const argoHelmConfigHelpers = `{{/*
Argo Configuration Preset Values (Influenced by Values configuration)
*/}}
{{- define "argo-cd.config.cm.presets" -}}
{{- $presets := dict -}}
{{- $_ := set $presets "url" (printf "https://%s" .Values.global.domain) -}}
{{- if eq (toString (index .Values.configs.cm "statusbadge.enabled")) "true" -}}
{{- $_ := set $presets "statusbadge.url" (printf "https://%s/" .Values.global.domain) -}}
{{- end -}}
{{- if .Values.configs.styles -}}
{{- $_ := set $presets "ui.cssurl" "./custom/custom.styles.css" -}}
{{- end -}}
{{- toYaml $presets }}
{{- end -}}

{{/*
Merge Argo Configuration with Preset Configuration
*/}}
{{- define "argo-cd.config.cm" -}}
{{- $config := omit .Values.configs.cm "create" "annotations" "resourceExclusionsAdditional" -}}
{{- $preset := include "argo-cd.config.cm.presets" . | fromYaml | default dict -}}
{{- $merged := mergeOverwrite $preset $config -}}
{{- if .Values.configs.cm.resourceExclusionsAdditional }}
{{- $existing := get $merged "resource.exclusions" | default "" | trimSuffix "\n" }}
{{- $additional := .Values.configs.cm.resourceExclusionsAdditional | toYaml }}
{{- $_ := set $merged "resource.exclusions" (list $existing $additional | compact | join "\n") -}}
{{- end }}
{{- range $key, $value := $merged }}
{{- $fmted := $value | toString }}
{{- if or (eq $key "url") (eq $key "statusbadge.url") }}
{{- $fmted = tpl $fmted $ }}
{{- end }}
{{- if not (eq $fmted "") }}
{{ $key }}: {{ $fmted | toYaml }}
{{- end }}
{{- end }}
{{- end -}}
`

// argoHelmConfigMapTemplate is templates/argocd-configs/argocd-cm.yaml of
// the same chart, without the labels, annotations, and namespace, which do
// not change the data.
const argoHelmConfigMapTemplate = `{{- if .Values.configs.cm.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  {{- include "argo-cd.config.cm" . | trim | nindent 2 }}
{{- end }}
`

// helmRenderedArgoCDConfigMap renders argocd-cm with the Helm v4 engine
// that Argo CD v3.6.1 uses, from chart defaults and one values file. It
// returns the data, whether the chart rendered argocd-cm, and the render
// error.
func helmRenderedArgoCDConfigMap(t *testing.T, defaults map[string]any, valuesYAML string) (map[string]string, bool, error) {
	t.Helper()
	chart := &chartv2.Chart{
		Metadata: &chartv2.Metadata{APIVersion: "v2", Name: "argo-cd", Version: "10.14.0"},
		Templates: []*common.File{
			{Name: "templates/_helpers.tpl", Data: []byte(argoHelmConfigHelpers)},
			{Name: "templates/argocd-cm.yaml", Data: []byte(argoHelmConfigMapTemplate)},
		},
		Values: map[string]any{
			"global":  map[string]any{"domain": "argocd.example.com"},
			"configs": map[string]any{"cm": defaults, "styles": ""},
		},
	}
	values, err := common.ReadValues([]byte(valuesYAML))
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}
	renderValues, err := chartutil.ToRenderValues(chart, values, common.ReleaseOptions{Name: "argocd", Namespace: "argocd"}, common.DefaultCapabilities)
	if err != nil {
		t.Fatalf("ToRenderValues() error = %v", err)
	}
	rendered, err := engine.Render(chart, renderValues)
	if err != nil {
		return nil, false, err
	}
	text := rendered["argo-cd/templates/argocd-cm.yaml"]
	if strings.TrimSpace(text) == "" {
		return nil, false, nil
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := sigsyaml.Unmarshal([]byte(text), &cm); err != nil {
		t.Fatalf("rendered argocd-cm does not decode: %v\n%s", err, text)
	}
	// The chart always adds url, which drydock does not read.
	delete(cm.Data, "url")
	return cm.Data, true, nil
}

// drydockHelmConfigMapData runs drydock's conversion on the values file
// through both the document loader path and the discovered object path,
// and checks that the two agree.
func drydockHelmConfigMapData(t *testing.T, valuesYAML string) (map[string]string, bool, bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(valuesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var doc helmValuesSettingsDocument
	if err := decodeYAMLDocumentAt(path, 0, &doc); err != nil {
		t.Fatalf("decodeYAMLDocumentAt() error = %v", err)
	}
	data, rendered, diags := helmArgoCDConfigMapData(doc.Configs.CM, path)

	raw, err := manifestyaml.NewDecoder(strings.NewReader(valuesYAML)).Decode()
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	object, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("values document = %#v, want a map", raw)
	}
	var objectDoc helmValuesSettingsDocument
	if err := decodeUnstructuredObject(&unstructured.Unstructured{Object: object}, &objectDoc); err != nil {
		t.Fatalf("decodeUnstructuredObject() error = %v", err)
	}
	objectData, objectRendered, objectDiags := helmArgoCDConfigMapData(objectDoc.Configs.CM, path)
	if !reflect.DeepEqual(objectData, data) || objectRendered != rendered || hasErrorDiagnostic(objectDiags) != hasErrorDiagnostic(diags) {
		t.Fatalf("object path = %#v %v %#v, document path = %#v %v %#v", objectData, objectRendered, objectDiags, data, rendered, diags)
	}
	return data, rendered, hasErrorDiagnostic(diags)
}

// TestHelmValuesConfigMapDataParityWithChart renders argocd-cm from one
// values file with the argo-helm chart helper and the Helm v4 engine, and
// checks that drydock builds the same argocd-cm data from that file.
func TestHelmValuesConfigMapDataParityWithChart(t *testing.T) {
	for _, tc := range []struct {
		name string
		// defaults are the chart's configs.cm defaults. drydock does not
		// read them, so only keys that the values file sets to null are
		// listed, besides create and annotations.
		defaults map[string]any
		values   string
		wantKey  string
	}{
		{name: "empty values are left out", values: `configs:
  cm:
    resource.customizations: |
      apps/Deployment:
        health.lua: |
          return { status = "Healthy" }
    resource.customizations.health.apps_Deployment: ""
    resource.customizations.ignoreDifferences.apps_Deployment: ""
    resource.customizations.useOpenLibs.example.com_Widget: ""
    resource.exclusions: ""
    application.instanceLabelKey: ""
    resource.ignoreResourceUpdatesEnabled: ""
    helm.valuesFileSchemes: ""
    cluster.inClusterEnabled: ""
    kustomize.buildOptions: --enable-helm
`, wantKey: "resource.customizations"},
		{name: "values become text as Helm writes them", values: `configs:
  cm:
    k.int: 100000000
    k.small: 3
    k.float: 1.50
    k.bool: true
    k.false: false
    k.zero: 0
    k.yes: yes
    k.on: on
    k.hex: 0x1F
    k.octal: 012
    k.date: 2024-01-02
    k.map:
      a: 1
      b: [x, 2]
    k.list: [1, "a", true]
    k.emptymap: {}
    k.emptylist: []
    k.space: " "
    k.bignum: 12345678901234567890
    resource.customizations.useOpenLibs.example.com_Widget: yes
    cluster.inClusterEnabled: off
`, wantKey: "k.int"},
		{name: "null removes chart defaults", defaults: map[string]any{
			"exec.enabled":                 false,
			"application.instanceLabelKey": "argocd.argoproj.io/instance",
		}, values: `configs:
  cm:
    exec.enabled: null
    application.instanceLabelKey: ~
    resource.compareoptions: |
      ignoreAggregatedRoles: true
`, wantKey: "resource.compareoptions"},
		{name: "chart keys are not copied", values: `configs:
  cm:
    create: true
    annotations:
      example.com/a: b
    resourceExclusionsAdditional: []
    application.resourceTrackingMethod: annotation
`, wantKey: "application.resourceTrackingMethod"},
		{name: "additional exclusions follow resource.exclusions", values: `configs:
  cm:
    resource.exclusions: |
      - apiGroups: [""]
        kinds: [Event]
    resourceExclusionsAdditional:
    - apiGroups: [cilium.io]
      kinds: [CiliumIdentity]
      clusters: ["*"]
`, wantKey: "resource.exclusions"},
		{name: "additional exclusions alone", values: `configs:
  cm:
    resourceExclusionsAdditional:
    - apiGroups: [cilium.io]
      kinds: [CiliumIdentity]
`, wantKey: "resource.exclusions"},
		{name: "create false renders no argocd-cm", values: `configs:
  cm:
    create: false
    resource.customizations.health.apps_Deployment: |
      return { status = "Healthy" }
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaults := map[string]any{"create": true, "annotations": map[string]any{}}
			maps.Copy(defaults, tc.defaults)
			want, wantRendered, err := helmRenderedArgoCDConfigMap(t, defaults, tc.values)
			if err != nil {
				t.Fatalf("Helm render error = %v", err)
			}
			got, gotRendered, gotErr := drydockHelmConfigMapData(t, tc.values)
			if gotErr {
				t.Fatalf("drydock reported an error; Helm rendered %#v", want)
			}
			if gotRendered != wantRendered {
				t.Fatalf("drydock rendered = %v, Helm rendered = %v", gotRendered, wantRendered)
			}
			if len(got) == 0 && len(want) == 0 {
				got, want = nil, nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argocd-cm data mismatch\ndrydock: %#v\nHelm:    %#v", got, want)
			}
			if tc.wantKey != "" {
				if _, ok := want[tc.wantKey]; !ok {
					t.Fatalf("Helm data %#v has no key %q", want, tc.wantKey)
				}
			}
		})
	}
}

// TestHelmValuesNonTextExclusionsWithAdditionalFails covers the one input
// where the chart helper fails: resource.exclusions that is not text while
// resourceExclusionsAdditional is set. drydock reports an error and reads
// no argocd-cm settings from the file.
func TestHelmValuesNonTextExclusionsWithAdditionalFails(t *testing.T) {
	values := `configs:
  cm:
    resource.exclusions:
    - kinds: [Event]
    resourceExclusionsAdditional:
    - kinds: [Lease]
`
	if _, _, err := helmRenderedArgoCDConfigMap(t, map[string]any{"create": true}, values); err == nil {
		t.Fatalf("Helm rendered the chart; move the case to the parity table")
	}
	if _, _, gotErr := drydockHelmConfigMapData(t, values); !gotErr {
		t.Fatalf("drydock reported no error")
	}
}

// TestHelmValuesNullKeyWithoutChartDefault records the one known gap. Helm
// keeps a null value for a key that the chart does not define, and toString
// writes it as "<nil>". drydock does not read the chart's defaults and
// always leaves a null key out.
func TestHelmValuesNullKeyWithoutChartDefault(t *testing.T) {
	values := "configs:\n  cm:\n    example.key: null\n"
	want, _, err := helmRenderedArgoCDConfigMap(t, map[string]any{"create": true}, values)
	if err != nil {
		t.Fatalf("Helm render error = %v", err)
	}
	if want["example.key"] != "<nil>" {
		t.Fatalf("Helm data = %#v, want example.key: <nil>; update the gap in docs/compatibility.md", want)
	}
	got, _, _ := drydockHelmConfigMapData(t, values)
	if _, ok := got["example.key"]; ok {
		t.Fatalf("drydock data = %#v, want no example.key", got)
	}
}

// TestHelmValuesResourceOverridesParityWithArgoCD renders argocd-cm from a
// values file with the chart helper, loads the rendered data with Argo CD
// v3.6.1 GetResourceOverrides, and checks that drydock's Helm values loader
// builds the same override map.
func TestHelmValuesResourceOverridesParityWithArgoCD(t *testing.T) {
	values := `configs:
  cm:
    resource.customizations: |
      apps/Deployment:
        health.lua: |
          return { status = "Healthy" }
        ignoreDifferences: |
          jsonPointers: [/spec/replicas]
    resource.customizations.health.apps_Deployment: ""
    resource.customizations.ignoreDifferences.apps_Deployment: ""
    resource.customizations.health.example.com_Widget: |
      return { status = "Healthy" }
    resource.customizations.useOpenLibs.example.com_Widget: ""
    resource.customizations.useOpenLibs.example.com_Gadget: yes
`
	data, rendered, err := helmRenderedArgoCDConfigMap(t, map[string]any{"create": true}, values)
	if err != nil || !rendered {
		t.Fatalf("Helm render error = %v, rendered = %v", err, rendered)
	}
	upstream, upstreamErr, panicked := upstreamResourceOverrides(t, data)
	if upstreamErr != nil || panicked {
		t.Fatalf("Argo CD error = %v, panicked = %v", upstreamErr, panicked)
	}

	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(values), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	for name, load := range map[string]func() (ArgoSettings, error){
		"LoadFromHelmValues": func() (ArgoSettings, error) {
			settings, diags, err := LoadFromHelmValues(path)
			if hasErrorDiagnostic(diags) {
				t.Fatalf("diagnostics = %#v", diags)
			}
			return settings, err
		},
		"LoadFromHelmValuesDocument": func() (ArgoSettings, error) {
			settings, diags, err := LoadFromHelmValuesDocument(path, 0)
			if hasErrorDiagnostic(diags) {
				t.Fatalf("diagnostics = %#v", diags)
			}
			return settings, err
		},
	} {
		settings, err := load()
		if err != nil {
			t.Fatalf("%s() error = %v", name, err)
		}
		want := comparableUpstreamOverrides(upstream)
		got := comparableDrydockCustomizations(settings.ResourceCustomizations)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s override map mismatch\ndrydock: %#v\nArgo CD: %#v", name, got, want)
		}
		if _, ok := want["apps/Deployment"]; !ok {
			t.Fatalf("Argo CD map %#v has no apps/Deployment", want)
		}
	}
}
