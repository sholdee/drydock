package appset

import (
	"encoding/json"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests compare drydock generator params with the params that Argo CD
// v3.6.1 builds. The upstream-shaped params copy the cited upstream generator
// code. The tests require the same values and the same Go types. They then
// run upstream Render on both and require the same output or the same error.
// Go types matter because Sprig map functions such as hasKey, keys, pick,
// omit, values, and dig take map[string]any. They fail on the
// map[string]string maps that Argo CD passes.

// upstreamTypeProbeTemplates holds Go templates whose result depends on the
// param Go types. Each one runs with missingkey=error, the option that most
// ApplicationSets set.
var upstreamTypeProbeTemplates = []string{
	`{{ hasKey .metadata.annotations "parity.drydock.dev/team" }}`,
	`{{ hasKey .metadata.labels "env" }}`,
	`{{ keys .metadata.labels | sortAlpha | join "," }}`,
	`{{ pick .metadata.labels "env" | toJson }}`,
	`{{ omit .metadata.labels "env" | toJson }}`,
	`{{ values .metadata.annotations | sortAlpha | join "," }}`,
	`{{ hasKey .values "region" }}`,
	`{{ dig "region" "none" .values }}`,
	`{{ .metadata.labels.env }}`,
	`{{ index .metadata.annotations "parity.drydock.dev/team" }}`,
	`{{ index .metadata.annotations "missing.example/key" | quote }}`,
	`{{ .values.region }}`,
	`{{ toJson .metadata }}`,
	`{{ range $k, $v := .metadata.labels }}{{ $k }}={{ $v }};{{ end }}`,
}

// upstreamTypeProbeMustFail lists the probes that must fail on the upstream
// params. This proves the oracle exercises the type difference: if Argo CD
// ever changes these maps to map[string]any, this test fails and the
// expectation must be revisited.
var upstreamTypeProbeMustFail = []string{
	`{{ hasKey .metadata.annotations "parity.drydock.dev/team" }}`,
	`{{ hasKey .metadata.labels "env" }}`,
	`{{ keys .metadata.labels | sortAlpha | join "," }}`,
	`{{ pick .metadata.labels "env" | toJson }}`,
	`{{ omit .metadata.labels "env" | toJson }}`,
	`{{ values .metadata.annotations | sortAlpha | join "," }}`,
	`{{ hasKey .values "region" }}`,
	`{{ dig "region" "none" .values }}`,
}

const upstreamParityNamespace = "argocd"

func upstreamParityClusterSecrets() []*corev1.Secret {
	return []*corev1.Secret{
		{
			Name:      "cluster-alpha",
			Namespace: upstreamParityNamespace,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
				"parity":                         "yes",
				"env":                            "prod",
			},
			Annotations: map[string]string{
				"parity.drydock.dev/team":      "alpha-team",
				argoappv1.AnnotationKeyRefresh: "2026-01-01T00:00:00Z",
			},
			Data: map[string][]byte{
				"name":    []byte("alpha"),
				"server":  []byte("https://alpha.example.invalid"),
				"project": []byte("platform"),
			},
		},
		{
			Name:      "cluster-beta",
			Namespace: upstreamParityNamespace,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
				"parity":                         "yes",
			},
			Data: map[string][]byte{
				"name":   []byte("beta"),
				"server": []byte("https://beta.example.invalid"),
			},
		},
	}
}

func upstreamParityClusterInputs(secrets []*corev1.Secret) []ClusterInput {
	inputs := make([]ClusterInput, 0, len(secrets))
	for _, secret := range secrets {
		inputs = append(inputs, ClusterInput{
			Name:        string(secret.Data["name"]),
			Server:      string(secret.Data["server"]),
			Project:     string(secret.Data["project"]),
			Labels:      secret.Labels,
			Annotations: secret.Annotations,
		})
	}
	return inputs
}

func upstreamParityAppSet(goTemplate bool) *argoappv1.ApplicationSet {
	appSet := &argoappv1.ApplicationSet{
		Name: "param-types", Namespace: upstreamParityNamespace,
		Spec: argoappv1.ApplicationSetSpec{GoTemplate: goTemplate},
	}
	if goTemplate {
		appSet.Spec.GoTemplateOptions = []string{"missingkey=error"}
	}
	return appSet
}

// upstreamClusterGeneratorParams builds the param sets that the Argo CD
// v3.6.1 cluster generator builds for the given Secrets. drydock cannot import
// applicationset/generators: its services package needs modules outside this
// module graph. So this helper copies the upstream code statement by
// statement:
//
//   - applicationset/generators/cluster.go:78-87 (GenerateParams loop)
//   - applicationset/generators/cluster.go:119-126 (paramHolder.consolidate)
//   - applicationset/generators/cluster.go:152-189 (getClusterParameters)
//   - applicationset/generators/value_interpolation.go:8-33
//     (appendTemplatedValues)
//
// Both Secrets match the selector in these tests, so this helper does not
// filter. The selector also stops Argo CD from adding the local in-cluster
// entry (cluster.go:89-104), so the helper leaves it out. The reserved
// annotation set is drydock's excludedClusterAnnotations, which mirrors
// cluster.go:132-140 and has its own tests.
func upstreamClusterGeneratorParams(t *testing.T, generator *argoappv1.ClusterGenerator, appSet *argoappv1.ApplicationSet) []map[string]any {
	t.Helper()
	secrets := upstreamParityClusterSecrets()
	res := make([]map[string]any, 0, len(secrets))
	for _, cluster := range secrets {
		params := map[string]any{}
		params["name"] = string(cluster.Data["name"])
		params["nameNormalized"] = appsetutils.SanitizeName(string(cluster.Data["name"]))
		params["server"] = string(cluster.Data["server"])
		project, ok := cluster.Data["project"]
		if ok {
			params["project"] = string(project)
		} else {
			params["project"] = ""
		}
		annotations := upstreamClusterAnnotationParams(cluster.Annotations)
		if appSet.Spec.GoTemplate {
			meta := map[string]any{}
			if len(cluster.Annotations) > 0 {
				meta["annotations"] = annotations
			}
			if len(cluster.Labels) > 0 {
				meta["labels"] = cluster.Labels
			}
			params["metadata"] = meta
		} else {
			for key, value := range annotations {
				params["metadata.annotations."+key] = value
			}
			for key, value := range cluster.Labels {
				params["metadata.labels."+key] = value
			}
		}
		upstreamAppendTemplatedValues(t, generator.Values, params, appSet.Spec.GoTemplate, appSet.Spec.GoTemplateOptions)
		res = append(res, params)
	}
	if generator.FlatList {
		res = []map[string]any{{"clusters": res}}
	}
	return res
}

// upstreamClusterAnnotationParams copies
// applicationset/generators/cluster.go:142-150.
func upstreamClusterAnnotationParams(annotations map[string]string) map[string]string {
	params := make(map[string]string, len(annotations))
	for key, value := range annotations {
		if !excludedClusterAnnotations[key] {
			params[key] = value
		}
	}
	return params
}

// upstreamAppendTemplatedValues copies
// applicationset/generators/value_interpolation.go:8-33. Upstream calls
// render.Replace, which is the same appsetutils.Render.Replace used here.
func upstreamAppendTemplatedValues(t *testing.T, values map[string]string, params map[string]any, useGoTemplate bool, goTemplateOptions []string) {
	t.Helper()
	tmp := map[string]any{}
	for key, value := range values {
		result, err := (&appsetutils.Render{}).Replace(value, params, useGoTemplate, goTemplateOptions)
		if err != nil {
			t.Fatalf("upstream appendTemplatedValues(%s) error = %v", key, err)
		}
		if useGoTemplate {
			if tmp["values"] == nil {
				tmp["values"] = map[string]string{}
			}
			rendered, ok := tmp["values"].(map[string]string)
			if !ok {
				t.Fatalf("upstream values param is %T", tmp["values"])
			}
			rendered[key] = result
		} else {
			tmp["values."+key] = result
		}
	}
	maps.Copy(params, tmp)
}

func drydockClusterGeneratorParams(t *testing.T, generator *argoappv1.ClusterGenerator, appSet *argoappv1.ApplicationSet) []map[string]any {
	t.Helper()
	paramSets, diags, err := clusterGeneratorParamSets("app-set.yaml", generator, upstreamParityClusterInputs(upstreamParityClusterSecrets()), appSet.Spec.GoTemplate, appSet.Spec.GoTemplateOptions)
	if err != nil || len(diags) != 0 {
		t.Fatalf("clusterGeneratorParamSets() error = %v, diagnostics = %#v", err, diags)
	}
	out := make([]map[string]any, 0, len(paramSets))
	for _, paramSet := range paramSets {
		out = append(out, paramSet.Params)
	}
	return out
}

func sortParamsByName(params []map[string]any) {
	sort.Slice(params, func(i, j int) bool {
		return paramName(params[i]) < paramName(params[j])
	})
}

func paramName(params map[string]any) string {
	if name, ok := params["name"].(string); ok {
		return name
	}
	if path, ok := params["path"].(map[string]any); ok {
		if value, ok := path["path"].(string); ok {
			return value
		}
	}
	return ""
}

// assertSameUpstreamRender runs upstream Render.Replace with each template on
// the upstream and drydock params and requires the same output or error.
func assertSameUpstreamRender(t *testing.T, label string, upstream, drydock map[string]any, goTemplate bool, templates []string, mustFail []string) {
	t.Helper()
	options := []string{"missingkey=error"}
	if !goTemplate {
		options = nil
	}
	renderer := &appsetutils.Render{}
	for _, tmpl := range templates {
		wantOut, wantErr := renderer.Replace(tmpl, upstream, goTemplate, options)
		gotOut, gotErr := renderer.Replace(tmpl, drydock, goTemplate, options)
		if (wantErr == nil) != (gotErr == nil) || wantOut != gotOut {
			t.Fatalf("%s template %s:\nupstream = %q, err %v\ndrydock  = %q, err %v", label, tmpl, wantOut, wantErr, gotOut, gotErr)
		}
		if wantErr != nil && wantErr.Error() != gotErr.Error() {
			t.Fatalf("%s template %s error differs:\nupstream: %v\ndrydock:  %v", label, tmpl, wantErr, gotErr)
		}
		if slices.Contains(mustFail, tmpl) && wantErr == nil {
			t.Fatalf("%s template %s rendered %q on upstream params; the oracle expects a type error", label, tmpl, wantOut)
		}
	}
}

func TestClusterGeneratorParamTypesMatchUpstream(t *testing.T) {
	for _, goTemplate := range []bool{true, false} {
		name := "fasttemplate"
		valuesTemplate := "{{name}}-east"
		if goTemplate {
			name = "goTemplate"
			valuesTemplate = "{{ .name }}-east"
		}
		t.Run(name, func(t *testing.T) {
			generator := &argoappv1.ClusterGenerator{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"parity": "yes"}},
				Values:   map[string]string{"region": valuesTemplate},
			}
			appSet := upstreamParityAppSet(goTemplate)
			upstream := upstreamClusterGeneratorParams(t, generator, appSet)
			drydock := drydockClusterGeneratorParams(t, generator, appSet)
			sortParamsByName(upstream)
			sortParamsByName(drydock)
			// reflect.DeepEqual compares dynamic types, so map[string]string
			// and map[string]any with the same entries are not equal.
			if !reflect.DeepEqual(drydock, upstream) {
				t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", drydock, upstream)
			}

			if goTemplate {
				// alpha has annotations, labels, and values, so every probe
				// reaches a real map.
				assertSameUpstreamRender(t, "cluster alpha", upstream[0], drydock[0], true, upstreamTypeProbeTemplates, upstreamTypeProbeMustFail)
				// beta has no annotations, so probes on the missing key fail
				// on both sides.
				assertSameUpstreamRender(t, "cluster beta", upstream[1], drydock[1], true, upstreamTypeProbeTemplates, nil)
				return
			}
			fastTemplates := []string{
				"{{metadata.annotations.parity.drydock.dev/team}}",
				"{{metadata.annotations.argocd.argoproj.io/refresh}}",
				"{{metadata.labels.env}}",
				"{{values.region}}",
				"{{metadata.labels}}",
			}
			for i := range upstream {
				assertSameUpstreamRender(t, "cluster "+paramName(upstream[i]), upstream[i], drydock[i], false, fastTemplates, nil)
			}
		})
	}
}

func TestClusterGeneratorFlatListParamTypesMatchUpstream(t *testing.T) {
	generator := &argoappv1.ClusterGenerator{
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"parity": "yes"}},
		FlatList: true,
	}
	appSet := upstreamParityAppSet(true)
	upstream := upstreamClusterGeneratorParams(t, generator, appSet)
	drydock := drydockClusterGeneratorParams(t, generator, appSet)
	if len(upstream) != 1 || len(drydock) != 1 {
		t.Fatalf("flat list param sets: upstream %d, drydock %d, want 1 each", len(upstream), len(drydock))
	}
	upstreamList, ok := upstream[0]["clusters"].([]map[string]any)
	if !ok {
		t.Fatalf("upstream clusters param is %T; the oracle expects []map[string]any", upstream[0]["clusters"])
	}
	drydockList, ok := drydock[0]["clusters"].([]map[string]any)
	if !ok {
		t.Fatalf("drydock clusters param is %T, want []map[string]any", drydock[0]["clusters"])
	}
	sortParamsByName(upstreamList)
	sortParamsByName(drydockList)
	if !reflect.DeepEqual(drydockList, upstreamList) {
		t.Fatalf("drydock flat list differs from upstream:\ndrydock  = %#v\nupstream = %#v", drydockList, upstreamList)
	}
}

// TestClusterGeneratorSprigMapFunctionFailsLikeArgoCD checks the whole
// generation path: a template that calls hasKey on .metadata.annotations
// makes the ApplicationSet produce zero Applications, as Argo CD does.
func TestClusterGeneratorSprigMapFunctionFailsLikeArgoCD(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - clusters:
        selector:
          matchLabels:
            parity: "yes"
  template:
    metadata:
      name: 'cluster-{{ .name }}'
      annotations:
        team: '{{ if hasKey .metadata.annotations "parity.drydock.dev/team" }}set{{ else }}unset{{ end }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{ .name }}
        targetRevision: main
      destination:
        server: '{{ .server }}'
        namespace: default
`)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: upstreamParityClusterInputs(upstreamParityClusterSecrets()),
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated %d Applications (%v), want none", len(apps), generatedNames(apps))
	}
	// drydock reports each failing param set. alpha fails on the
	// map[string]string type and beta fails because it has no annotations.
	sawTypeError := false
	for _, diag := range diags {
		if code := diagnostic.StableCode(diag); code != "appset.template-render-failed" {
			t.Fatalf("diagnostic code = %q, want appset.template-render-failed: %#v", code, diag)
		}
		sawTypeError = sawTypeError || strings.Contains(diag.Message, "got map[string]string")
	}
	if !sawTypeError {
		t.Fatalf("diagnostics = %#v, want the hasKey map[string]string type error", diags)
	}
}

func TestGitDirectoryValuesParamTypesMatchUpstream(t *testing.T) {
	appSet := upstreamParityAppSet(true)
	values := map[string]string{"region": "{{ .path.basename }}-east"}
	appPath := "apps/alpha"
	// applicationset/generators/repo_path_utils.go:128-155
	// (buildPathParameters, Go-template branch), then
	// value_interpolation.go:8-33. The Git generator is not provider-backed,
	// but it shares the values helper with the provider generators in Argo CD
	// and in drydock.
	upstream := map[string]any{}
	paramPath := map[string]any{}
	paramPath["path"] = appPath
	paramPath["basename"] = path.Base(appPath)
	paramPath["basenameNormalized"] = appsetutils.SanitizeName(path.Base(appPath))
	paramPath["segments"] = strings.Split(appPath, "/")
	upstream["path"] = paramPath
	upstreamAppendTemplatedValues(t, values, upstream, true, appSet.Spec.GoTemplateOptions)

	drydock, err := pathParams(appPath, "", values, true, appSet.Spec.GoTemplateOptions)
	if err != nil {
		t.Fatalf("pathParams() error = %v", err)
	}
	if !reflect.DeepEqual(drydock, upstream) {
		t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", drydock, upstream)
	}
	assertSameUpstreamRender(t, "git directory", upstream, drydock, true, []string{
		`{{ hasKey .values "region" }}`,
		`{{ .values.region }}`,
	}, []string{`{{ hasKey .values "region" }}`})
}

func TestPluginGeneratorParamTypesMatchUpstream(t *testing.T) {
	output := map[string]any{
		"environment": "prod",
		"replicas":    float64(3),
		"cluster":     map[string]any{"name": "alpha"},
	}
	var inputParameters argoappv1.PluginParameters
	if err := json.Unmarshal([]byte(`{"team":"alpha-team"}`), &inputParameters); err != nil {
		t.Fatalf("decode plugin input parameters: %v", err)
	}
	values := map[string]string{"region": "{{ .environment }}-east"}
	options := []string{"missingkey=error"}

	// applicationset/generators/plugin.go:112-145 (generateParams,
	// Go-template branch), then value_interpolation.go:8-33.
	upstream := map[string]any{}
	maps.Copy(upstream, output)
	upstream["generator"] = map[string]any{
		"input": map[string]argoappv1.PluginParameters{
			"parameters": inputParameters,
		},
	}
	upstreamAppendTemplatedValues(t, values, upstream, true, options)

	drydock, err := pluginParams(output, inputParameters, values, true, options)
	if err != nil {
		t.Fatalf("pluginParams() error = %v", err)
	}
	if !reflect.DeepEqual(drydock, upstream) {
		t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", drydock, upstream)
	}
	assertSameUpstreamRender(t, "plugin", upstream, drydock, true, []string{
		`{{ hasKey .values "region" }}`,
		`{{ .values.region }}`,
		`{{ hasKey .cluster "name" }}`,
	}, []string{`{{ hasKey .values "region" }}`})
}

// The SCM provider, pull request, and clusterDecisionResource tests check
// the Go types that the cited upstream lines produce.

func TestPullRequestParamTypesMatchUpstream(t *testing.T) {
	input := PullRequestInput{Number: 7, Branch: "feature", TargetBranch: "main", HeadSHA: "abcdef1234567890", Labels: []string{"preview"}}
	params, err := pullRequestParams(input, map[string]string{"region": "{{ .branch }}-east"}, true, []string{"missingkey=error"})
	if err != nil {
		t.Fatalf("pullRequestParams() error = %v", err)
	}
	// applicationset/generators/pull_request.go:117 sets labels to the
	// provider's []string.
	if _, ok := params["labels"].([]string); !ok {
		t.Fatalf("labels param is %T, want []string", params["labels"])
	}
	// applicationset/generators/value_interpolation.go:22 stores values as
	// map[string]string.
	if _, ok := params["values"].(map[string]string); !ok {
		t.Fatalf("values param is %T, want map[string]string", params["values"])
	}
}

func TestSCMRepositoryValuesParamTypeMatchesUpstream(t *testing.T) {
	input := SCMRepositoryInput{Organization: "example", Repository: "repo", Branch: "main", SHA: "abcdef1234567890"}
	params, err := scmRepositoryParams(input, map[string]string{"region": "{{ .repository }}-east"}, true, []string{"missingkey=error"})
	if err != nil {
		t.Fatalf("scmRepositoryParams() error = %v", err)
	}
	// applicationset/generators/scm_provider.go:288 uses the shared
	// appendTemplatedValues, which stores map[string]string.
	if _, ok := params["values"].(map[string]string); !ok {
		t.Fatalf("values param is %T, want map[string]string", params["values"])
	}
}

func TestClusterDecisionResourceValuesParamTypeMatchesUpstream(t *testing.T) {
	generator := &argoappv1.DuckTypeGenerator{Values: map[string]string{"region": "east"}}
	clusters := map[string]ClusterInput{"alpha": {Name: "alpha", Server: "https://alpha.example.invalid"}}
	paramSet, ok, err := clusterDecisionResourceDecisionParamSet(generator, "clusterName", map[string]any{"clusterName": "alpha"}, clusters, true, []string{"missingkey=error"})
	if err != nil || !ok {
		t.Fatalf("clusterDecisionResourceDecisionParamSet() ok = %v, error = %v", ok, err)
	}
	// applicationset/generators/duck_type.go:222-230 (collectParams) stores
	// values as map[string]string.
	upstream := map[string]any{
		"name":        "alpha",
		"server":      "https://alpha.example.invalid",
		"clusterName": "alpha",
		"values":      map[string]string{"region": "east"},
	}
	if !reflect.DeepEqual(paramSet.Params, upstream) {
		t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", paramSet.Params, upstream)
	}
	assertSameUpstreamRender(t, "clusterDecisionResource", upstream, paramSet.Params, true, []string{
		`{{ hasKey .values "region" }}`,
		`{{ .values.region }}`,
	}, []string{`{{ hasKey .values "region" }}`})
}

// TestMatrixKeepsUpstreamParamTypes checks that the matrix merge keeps the
// map[string]string params and does not share them between param sets.
func TestMatrixKeepsUpstreamParamTypes(t *testing.T) {
	labels := map[string]string{"env": "prod"}
	first := map[string]any{"metadata": map[string]any{"labels": labels}, "values": map[string]string{"region": "east"}}
	second := map[string]any{"path": map[string]any{"segments": []string{"apps", "alpha"}}}
	combined, err := combineMatrixParams(first, second, true)
	if err != nil {
		t.Fatalf("combineMatrixParams() error = %v", err)
	}
	metadata, ok := combined["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("matrix metadata is %T, want map[string]any", combined["metadata"])
	}
	gotLabels, ok := metadata["labels"].(map[string]string)
	if !ok {
		t.Fatalf("matrix metadata.labels is %T, want map[string]string", metadata["labels"])
	}
	if _, ok := combined["values"].(map[string]string); !ok {
		t.Fatalf("matrix values is %T, want map[string]string", combined["values"])
	}
	gotLabels["env"] = "changed"
	if labels["env"] != "prod" {
		t.Fatal("matrix merge shares the metadata.labels map with its input")
	}
}
