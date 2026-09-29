package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// The expected strings in this file were produced by the helm v4.2.1 CLI — the
// helm that quay.io/argoproj/argocd:v3.5.3 bundles (argo-cd hack/tool-versions.sh)
// — invoked with the arguments Argo CD's repo-server builds for the same
// Application (util/helm/cmd.go template): --set/--set-string/--set-file per
// parameter, --values per resolved valueFile, then --values <temp file holding
// helm.values verbatim, or sigs.k8s.io/yaml.JSONToYAML(helm.valuesObject)>.
//
// Helm reads every --values file and the chart's values.yaml through
// sigs.k8s.io/yaml (JSON), so YAML numbers become float64 and a template that
// prints {{ .Values.big }} renders 1000000 as 1e+06; --set goes through strvals
// and keeps int64, --set-string and --set-file keep strings.

const helmValuesParityRepoURL = "https://git.example.test/org/helm-values-parity.git"

const helmValuesParityNumbers = `
  big: 1000000
  mid: 1234567
  huge: 12345678901
  small: 42
  float: 1.5
  exp: 1e6
  str: "1000000"
  nested:
    big: 1000000
  list:
    - 1000000
`

const helmValuesParityNumbersTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: nums
data:
{{- range $ch := list "chart" "vf" "ref" "inline" "p" "ps" "pf" }}
{{- $v := index $.Values $ch }}
{{- if $v }}
{{- range $k := list "big" "mid" "huge" "small" "float" "exp" "str" }}
{{- if hasKey $v $k }}
  {{ $ch }}.{{ $k }}: "{{ index $v $k }}"
{{- end }}
{{- end }}
{{- if hasKey $v "nested" }}
  {{ $ch }}.nested.big: "{{ $v.nested.big }}"
{{- end }}
{{- if hasKey $v "list" }}
  {{ $ch }}.list0: "{{ index $v.list 0 }}"
{{- end }}
{{- end }}
{{- end }}
`

func TestRenderApplicationHelmValuesNumbersMatchHelmCLI(t *testing.T) {
	// helm v4.2.1 output, identical for the valuesObject and values cases.
	want := map[string]string{
		// chart values.yaml, valueFiles, $ref valueFiles, and the inline
		// values temp file: all --values-style reads, all float64.
		"chart.big": "1e+06", "chart.mid": "1.234567e+06", "chart.huge": "1.2345678901e+10",
		"chart.small": "42", "chart.float": "1.5", "chart.exp": "1e+06", "chart.str": "1000000",
		"chart.nested.big": "1e+06", "chart.list0": "1e+06",
		"vf.big": "1e+06", "vf.mid": "1.234567e+06", "vf.huge": "1.2345678901e+10",
		"vf.small": "42", "vf.float": "1.5", "vf.exp": "1e+06", "vf.str": "1000000",
		"vf.nested.big": "1e+06", "vf.list0": "1e+06",
		"ref.big": "1e+06", "ref.mid": "1.234567e+06", "ref.huge": "1.2345678901e+10",
		"ref.small": "42", "ref.float": "1.5", "ref.exp": "1e+06", "ref.str": "1000000",
		"ref.nested.big": "1e+06", "ref.list0": "1e+06",
		"inline.big": "1e+06", "inline.mid": "1.234567e+06", "inline.huge": "1.2345678901e+10",
		"inline.small": "42", "inline.float": "1.5", "inline.exp": "1e+06", "inline.str": "1000000",
		"inline.nested.big": "1e+06", "inline.list0": "1e+06",
		// parameters (--set): strvals keeps integers int64, floats as strings.
		"p.big": "1000000", "p.mid": "1234567", "p.huge": "12345678901",
		"p.small": "42", "p.float": "1.5", "p.exp": "1e6",
		// forceString parameters (--set-string) and fileParameters (--set-file).
		"ps.big": "1000000", "ps.small": "42",
		"pf.big": "1000000",
	}

	for _, tt := range []struct {
		name string
		set  func(*argoappv1.ApplicationSourceHelm)
	}{
		{
			name: "valuesObject",
			set: func(helm *argoappv1.ApplicationSourceHelm) {
				// The JSON the API server stores for the equivalent YAML valuesObject.
				helm.ValuesObject = &runtime.RawExtension{Raw: []byte(`{"inline":{"big":1000000,"mid":1234567,"huge":12345678901,"small":42,"float":1.5,"exp":1e+06,"str":"1000000","nested":{"big":1000000},"list":[1000000]}}`)}
			},
		},
		{
			name: "values",
			set: func(helm *argoappv1.ApplicationSourceHelm) {
				helm.Values = "inline:" + helmValuesParityNumbers
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: nums\nversion: 0.1.0\n")
			writeTestFile(t, filepath.Join(root, "chart", "values.yaml"), "chart:"+helmValuesParityNumbers)
			writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), helmValuesParityNumbersTemplate)
			writeTestFile(t, filepath.Join(root, "chart", "extra-values.yaml"), "vf:"+helmValuesParityNumbers)
			writeTestFile(t, filepath.Join(root, "refvals", "ref-values.yaml"), "ref:"+helmValuesParityNumbers)
			writeTestFile(t, filepath.Join(root, "chart", "pf-big.txt"), "1000000")

			helm := &argoappv1.ApplicationSourceHelm{
				ValueFiles: []string{"extra-values.yaml", "$vals/refvals/ref-values.yaml"},
				Parameters: []argoappv1.HelmParameter{
					{Name: "p.big", Value: "1000000"},
					{Name: "p.mid", Value: "1234567"},
					{Name: "p.huge", Value: "12345678901"},
					{Name: "p.small", Value: "42"},
					{Name: "p.float", Value: "1.5"},
					{Name: "p.exp", Value: "1e6"},
					{Name: "ps.big", Value: "1000000", ForceString: true},
					{Name: "ps.small", Value: "42", ForceString: true},
				},
				FileParameters: []argoappv1.HelmFileParameter{{Name: "pf.big", Path: "pf-big.txt"}},
			}
			tt.set(helm)
			got := renderHelmValuesParityConfigMap(t, root, helm)
			for key, wantValue := range want {
				if got[key] != wantValue {
					t.Errorf("data[%q] = %q, want %q (helm v4.2.1)", key, got[key], wantValue)
				}
			}
			if len(got) != len(want) {
				t.Errorf("rendered %d data keys, want %d: %v", len(got), len(want), got)
			}
		})
	}
}

func TestRenderApplicationHelmValuesYAMLMatchesHelmCLI(t *testing.T) {
	const template = `apiVersion: v1
kind: ConfigMap
metadata:
  name: nums
data:
{{- range $k, $v := .Values.d }}
  {{ $k }}: "{{ kindOf $v }}={{ $v }}"
{{- end }}
`
	// Scalars resolve as YAML 1.1 through sigs.k8s.io/yaml, timestamps stay
	// strings, duplicate keys resolve last-wins, and every document of the
	// stream is merged.
	const document = `d:
  yesv: yes
  nov: no
  onv: on
  ts: 2024-01-01
  dup: first
  dup: second
---
d:
  seconddoc: 1000000
`
	want := map[string]string{
		"yesv":      "bool=true",
		"nov":       "bool=false",
		"onv":       "bool=true",
		"ts":        "string=2024-01-01",
		"dup":       "string=second",
		"seconddoc": "float64=1e+06",
	}

	for _, tt := range []struct {
		name  string
		files map[string]string
		helm  argoappv1.ApplicationSourceHelm
		want  map[string]string
	}{
		{
			name:  "valueFiles",
			files: map[string]string{"override.yaml": document},
			helm:  argoappv1.ApplicationSourceHelm{ValueFiles: []string{"override.yaml"}},
			want:  want,
		},
		{
			name: "values",
			helm: argoappv1.ApplicationSourceHelm{Values: document},
			want: want,
		},
		{
			name: "comment-only values",
			helm: argoappv1.ApplicationSourceHelm{Values: "# d:\n#   x: 1\n"},
			want: map[string]string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: nums\nversion: 0.1.0\n")
			writeTestFile(t, filepath.Join(root, "chart", "values.yaml"), "d: {}\n")
			writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), template)
			for name, content := range tt.files {
				writeTestFile(t, filepath.Join(root, "chart", name), content)
			}
			helm := tt.helm
			got := renderHelmValuesParityConfigMap(t, root, &helm)
			for key, wantValue := range tt.want {
				if got[key] != wantValue {
					t.Errorf("data[%q] = %q, want %q (helm v4.2.1)", key, got[key], wantValue)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("rendered %d data keys, want %d: %v", len(got), len(tt.want), got)
			}
		})
	}
}

func renderHelmValuesParityConfigMap(t *testing.T, root string, helm *argoappv1.ApplicationSourceHelm) map[string]string {
	t.Helper()
	application := argoappv1.Application{
		Namespace: "argocd", Name: "nums",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "nums"},
			Sources: argoappv1.ApplicationSources{
				{RepoURL: helmValuesParityRepoURL, TargetRevision: "main", Path: "chart", Helm: helm},
				{RepoURL: helmValuesParityRepoURL, TargetRevision: "main", Ref: "vals"},
			},
		},
	}
	provider := localProvider{
		repoRoot: root,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{
			RepoMaps: []sourcepkg.RepoMap{{URL: helmValuesParityRepoURL, Path: root}},
		}),
	}
	result, err := RenderApplication(context.Background(), application, provider)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	var configMap *render.Manifest
	for i := range result.Manifests {
		if result.Manifests[i].Object.GetKind() == "ConfigMap" {
			configMap = &result.Manifests[i]
		}
	}
	if configMap == nil {
		t.Fatalf("no ConfigMap rendered: %#v", result.Manifests)
	}
	data := map[string]string{}
	raw, _, err := unstructured.NestedFieldNoCopy(configMap.Object.Object, "data")
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	if raw == nil {
		// An empty range renders `data:` (null), as helm does.
		return data
	}
	values, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want a mapping", raw)
	}
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("data[%q] = %#v, want a string", key, value)
		}
		data[key] = text
	}
	return data
}
