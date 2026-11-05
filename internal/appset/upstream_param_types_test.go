package appset

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/google/uuid"
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

// upstreamSCMRepository mirrors applicationset/services/scm_provider
// Repository (types.go:9-17). RepositoryId is an any that each provider sets
// with its own Go type.
type upstreamSCMRepository struct {
	Organization string
	Repository   string
	URL          string
	Branch       string
	SHA          string
	Labels       []string
	RepositoryId any
}

// upstreamSCMProviderParams copies applicationset/generators/scm_provider.go:268-292
// (the param build loop), then value_interpolation.go:8-33.
func upstreamSCMProviderParams(t *testing.T, repo upstreamSCMRepository, values map[string]string, goTemplate bool, options []string) map[string]any {
	t.Helper()
	shortSHALength := min(len(repo.SHA), 8)
	shortSHALength7 := min(len(repo.SHA), 7)
	params := map[string]any{
		"organization":     repo.Organization,
		"repository":       repo.Repository,
		"repository_id":    repo.RepositoryId,
		"url":              repo.URL,
		"branch":           repo.Branch,
		"sha":              repo.SHA,
		"short_sha":        repo.SHA[:shortSHALength],
		"short_sha_7":      repo.SHA[:shortSHALength7],
		"labels":           strings.Join(repo.Labels, ","),
		"branchNormalized": appsetutils.SanitizeName(repo.Branch),
	}
	upstreamAppendTemplatedValues(t, values, params, goTemplate, options)
	return params
}

// upstreamSCMRepositoryIDCases lists, per provider, the Go value that the
// Argo CD v3.6.1 provider stores in RepositoryId:
//
//   - GitHub: githubRepo.ID, *int64 (scm_provider/github.go:105; go-github
//     v69.2.0 Repository.ID)
//   - GitLab: gitlabRepo.ID, int64 (scm_provider/gitlab.go:138;
//     gitlab.com/gitlab-org/api/client-go v1.46.0 Project.ID)
//   - Gitea: int(repo.ID), int (scm_provider/gitea.go:129)
//   - Bitbucket Server: bitbucketRepo.ID, int
//     (scm_provider/bitbucket_server.go:107; go-bitbucket-v1 Repository.ID)
//   - Bitbucket Cloud: bitBucketRepo.Uuid, string
//     (scm_provider/bitbucket_cloud.go:118; ktrysmt/go-bitbucket v0.10.0)
//   - Azure DevOps: *azureRepo.Id, uuid.UUID (scm_provider/azure_devops.go:95)
//   - AWS CodeCommit: aws.ToString(RepositoryId), string
//     (scm_provider/aws_codecommit.go:120)
//
// The module versions are the ones in the Argo CD v3.6.1 go.mod.
var upstreamSCMRepositoryIDCases = []struct {
	name      string
	generator argoappv1.SCMProviderGenerator
	provider  string
	fixtureID string
	upstream  any
}{
	{"github", argoappv1.SCMProviderGenerator{Github: &argoappv1.SCMProviderGeneratorGithub{Organization: "example"}}, "github", "123456789", new(int64(123456789))},
	{"github empty", argoappv1.SCMProviderGenerator{Github: &argoappv1.SCMProviderGeneratorGithub{Organization: "example"}}, "github", "", (*int64)(nil)},
	{"gitlab", argoappv1.SCMProviderGenerator{Gitlab: &argoappv1.SCMProviderGeneratorGitlab{Group: "example"}}, "gitlab", "9007199254740993", int64(9007199254740993)},
	{"gitlab empty", argoappv1.SCMProviderGenerator{Gitlab: &argoappv1.SCMProviderGeneratorGitlab{Group: "example"}}, "gitlab", "", int64(0)},
	{"gitea", argoappv1.SCMProviderGenerator{Gitea: &argoappv1.SCMProviderGeneratorGitea{Owner: "example"}}, "gitea", "42", 42},
	{"bitbucket server", argoappv1.SCMProviderGenerator{BitbucketServer: &argoappv1.SCMProviderGeneratorBitbucketServer{Project: "example"}}, "bitbucketServer", "-7", -7},
	{"bitbucket cloud", argoappv1.SCMProviderGenerator{Bitbucket: &argoappv1.SCMProviderGeneratorBitbucket{Owner: "example"}}, "bitbucket", "{0b5a6f0e-2c3d-4e5f-8a9b-0c1d2e3f4a5b}", "{0b5a6f0e-2c3d-4e5f-8a9b-0c1d2e3f4a5b}"},
	{"azure devops", argoappv1.SCMProviderGenerator{AzureDevOps: &argoappv1.SCMProviderGeneratorAzureDevOps{Organization: "example", TeamProject: "example"}}, "azureDevOps", "6BA7B810-9DAD-11D1-80B4-00C04FD430C8", uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")},
	{"azure devops empty", argoappv1.SCMProviderGenerator{AzureDevOps: &argoappv1.SCMProviderGeneratorAzureDevOps{Organization: "example", TeamProject: "example"}}, "azureDevOps", "", uuid.Nil},
	{"aws codecommit", argoappv1.SCMProviderGenerator{AWSCodeCommit: &argoappv1.SCMProviderGeneratorAWSCodeCommit{Region: "us-east-1"}}, "awsCodeCommit", "f7579e13-b83e-4027-aaef-650c0EXAMPLE", "f7579e13-b83e-4027-aaef-650c0EXAMPLE"},
}

// upstreamSCMRepositoryIDTemplates prints repository_id in ways that depend
// on its Go type. printf "%v" is left out: on a *int64 it prints a memory
// address, which differs between any two runs in Argo CD and in drydock.
var upstreamSCMRepositoryIDTemplates = []string{
	`{{ .repository_id }}`,
	`{{ toJson .repository_id }}`,
	`{{ printf "%T" .repository_id }}`,
	`{{ eq .repository_id "123456789" }}`,
	`{{ kindOf .repository_id }}`,
	`{{ .values.id }}`,
}

func TestSCMRepositoryIDParamMatchesUpstream(t *testing.T) {
	for _, tc := range upstreamSCMRepositoryIDCases {
		for _, goTemplate := range []bool{true, false} {
			mode := "fasttemplate"
			values := map[string]string{"id": "{{repository_id}}"}
			templates := []string{"{{repository_id}}", "{{values.id}}", "{{repository}}"}
			var options []string
			if goTemplate {
				mode = "goTemplate"
				values = map[string]string{"id": "{{ .repository_id }}"}
				templates = upstreamSCMRepositoryIDTemplates
				options = []string{"missingkey=error"}
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				input := SCMRepositoryInput{Provider: tc.provider, Organization: "example", Project: "example", Region: "us-east-1", Repository: "repo", RepositoryID: tc.fixtureID, Branch: "main", SHA: "abcdef1234567890", Labels: []string{"ops"}}
				upstream := upstreamSCMProviderParams(t, upstreamSCMRepository{
					Organization: input.Organization,
					Repository:   input.Repository,
					Branch:       input.Branch,
					SHA:          input.SHA,
					Labels:       input.Labels,
					RepositoryId: tc.upstream,
				}, values, goTemplate, options)

				generator := tc.generator
				generator.Values = values
				paramSets, diags, err := scmProviderParamSets("app-set.yaml", &generator, []SCMRepositoryInput{input}, goTemplate, options)
				if err != nil || len(diags) != 0 || len(paramSets) != 1 {
					t.Fatalf("scmProviderParamSets() = %d param sets, diagnostics %#v, error %v", len(paramSets), diags, err)
				}
				drydock := paramSets[0].Params
				if !reflect.DeepEqual(drydock, upstream) {
					t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", drydock, upstream)
				}
				if got, want := reflect.TypeOf(drydock["repository_id"]), reflect.TypeOf(tc.upstream); got != want {
					t.Fatalf("repository_id type = %v, want %v", got, want)
				}
				assertSameUpstreamRender(t, "scmProvider "+tc.name, upstream, drydock, goTemplate, templates, nil)
			})
		}
	}
}

// TestSCMRepositoryIDOraclePrintsUpstreamValues pins what upstream Render
// prints for each Go type, so a change in the oracle shows up here.
func TestSCMRepositoryIDOraclePrintsUpstreamValues(t *testing.T) {
	want := map[string][2]string{
		"github":             {"123456789", "{{repository_id}}"},
		"github empty":       {"<nil>", "{{repository_id}}"},
		"gitlab":             {"9007199254740993", "{{repository_id}}"},
		"gitlab empty":       {"0", "{{repository_id}}"},
		"gitea":              {"42", "{{repository_id}}"},
		"bitbucket server":   {"-7", "{{repository_id}}"},
		"bitbucket cloud":    {"{0b5a6f0e-2c3d-4e5f-8a9b-0c1d2e3f4a5b}", "{0b5a6f0e-2c3d-4e5f-8a9b-0c1d2e3f4a5b}"},
		"azure devops":       {"6ba7b810-9dad-11d1-80b4-00c04fd430c8", "{{repository_id}}"},
		"azure devops empty": {"00000000-0000-0000-0000-000000000000", "{{repository_id}}"},
		"aws codecommit":     {"f7579e13-b83e-4027-aaef-650c0EXAMPLE", "f7579e13-b83e-4027-aaef-650c0EXAMPLE"},
	}
	renderer := &appsetutils.Render{}
	for _, tc := range upstreamSCMRepositoryIDCases {
		params := map[string]any{"repository_id": tc.upstream}
		goOut, err := renderer.Replace(`{{ .repository_id }}`, params, true, []string{"missingkey=error"})
		if err != nil {
			t.Fatalf("%s: Go template error = %v", tc.name, err)
		}
		fastOut, err := renderer.Replace(`{{repository_id}}`, params, false, nil)
		if err != nil {
			t.Fatalf("%s: fasttemplate error = %v", tc.name, err)
		}
		if got := [2]string{goOut, fastOut}; got != want[tc.name] {
			t.Fatalf("%s: upstream prints %q, want %q", tc.name, got, want[tc.name])
		}
	}
}

func TestSCMRepositoryIDInvalidFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		generator argoappv1.SCMProviderGenerator
		provider  string
		id        string
	}{
		{"github", argoappv1.SCMProviderGenerator{Github: &argoappv1.SCMProviderGeneratorGithub{Organization: "example"}}, "github", "repo-123"},
		{"gitlab", argoappv1.SCMProviderGenerator{Gitlab: &argoappv1.SCMProviderGeneratorGitlab{Group: "example"}}, "gitlab", "0x1F"},
		{"gitea", argoappv1.SCMProviderGenerator{Gitea: &argoappv1.SCMProviderGeneratorGitea{Owner: "example"}}, "gitea", "1.5"},
		{"bitbucket server", argoappv1.SCMProviderGenerator{BitbucketServer: &argoappv1.SCMProviderGeneratorBitbucketServer{Project: "example"}}, "bitbucketServer", " 7"},
		{"azure devops", argoappv1.SCMProviderGenerator{AzureDevOps: &argoappv1.SCMProviderGeneratorAzureDevOps{Organization: "example", TeamProject: "example"}}, "azureDevOps", "repo-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := SCMRepositoryInput{Provider: tc.provider, Organization: "example", Project: "example", Region: "us-east-1", Repository: "repo", RepositoryID: tc.id, Branch: "main", FixturePath: "fixtures.yaml"}
			paramSets, diags, err := scmProviderParamSets("app-set.yaml", &tc.generator, []SCMRepositoryInput{input}, true, nil)
			if err != nil || len(paramSets) != 0 || len(diags) != 1 {
				t.Fatalf("scmProviderParamSets() = %d param sets, diagnostics %#v, error %v; want one diagnostic", len(paramSets), diags, err)
			}
			diag := diags[0]
			if diag.Severity != diagnostic.SeverityError || diagnostic.StableCode(diag) != "appset.provider-fixture-invalid" || diag.Provenance.Path != "fixtures.yaml" || !strings.Contains(diag.Message, strconv.Quote(tc.id)) {
				t.Fatalf("diagnostic = %#v, want an appset.provider-fixture-invalid error for fixtures.yaml naming %q", diag, tc.id)
			}
		})
	}
}

// upstreamClusterDecisionParams copies the per-decision param build of the
// Argo CD v3.6.1 clusterDecisionResource generator:
//
//   - applicationset/generators/duck_type.go:160-169 (name, server, and each
//     decision field as fmt.Sprintf("%v"))
//   - applicationset/generators/duck_type.go:171-173 and collectParams at
//     :222-230 (values copied as raw strings, never rendered)
func upstreamClusterDecisionParams(cluster ClusterInput, clusterDecision map[string]any, values map[string]string, goTemplate bool) map[string]any {
	params := map[string]any{
		"name":   cluster.Name,
		"server": cluster.Server,
	}
	for key, value := range clusterDecision {
		params[key] = fmt.Sprintf("%v", value)
	}
	for key, value := range values {
		if goTemplate {
			if params["values"] == nil {
				params["values"] = map[string]string{}
			}
			params["values"].(map[string]string)[key] = value //nolint:errcheck // Copied upstream code; a panic here matches Argo CD.
		} else {
			params["values."+key] = value
		}
	}
	return params
}

// TestClusterDecisionResourceValuesMatchUpstream checks that drydock copies
// clusterDecisionResource values as raw strings, as Argo CD v3.6.1 does. A
// values template stays literal, and a values template that would fail to
// parse is not an error.
func TestClusterDecisionResourceValuesMatchUpstream(t *testing.T) {
	cluster := ClusterInput{Name: "alpha", Server: "https://alpha.example.invalid"}
	decision := map[string]any{"clusterName": "alpha", "region": "east", "weight": 3}
	clusters := map[string]ClusterInput{"alpha": cluster}
	for _, goTemplate := range []bool{true, false} {
		name := "fasttemplate"
		values := map[string]string{"region": "{{region}}", "broken": "{{ .missing ", "plain": "west"}
		templates := []string{"{{values.region}}", "{{values.broken}}", "{{values.plain}}", "{{region}}", "{{weight}}"}
		if goTemplate {
			name = "goTemplate"
			values = map[string]string{"region": "{{ .region }}", "broken": "{{ .missing ", "plain": "west"}
			templates = []string{
				`{{ .values.region }}`,
				`{{ .values.broken }}`,
				`{{ .values.plain }}`,
				`{{ .region }}`,
				`{{ .weight }}`,
				`{{ hasKey .values "region" }}`,
			}
		}
		t.Run(name, func(t *testing.T) {
			generator := &argoappv1.DuckTypeGenerator{Values: values}
			upstream := upstreamClusterDecisionParams(cluster, decision, values, goTemplate)
			paramSet, ok, err := clusterDecisionResourceDecisionParamSet(generator, "clusterName", decision, clusters, goTemplate)
			if err != nil || !ok {
				t.Fatalf("clusterDecisionResourceDecisionParamSet() ok = %v, error = %v", ok, err)
			}
			if !reflect.DeepEqual(paramSet.Params, upstream) {
				t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", paramSet.Params, upstream)
			}
			var mustFail []string
			if goTemplate {
				mustFail = []string{`{{ hasKey .values "region" }}`}
			}
			assertSameUpstreamRender(t, "clusterDecisionResource", upstream, paramSet.Params, goTemplate, templates, mustFail)
			// The oracle must show the raw value, or this test would not
			// catch a drydock that renders values again.
			got, _ := upstream["values.region"].(string)
			want := values["region"]
			if goTemplate {
				valuesParam, _ := upstream["values"].(map[string]string)
				got = valuesParam["region"]
			}
			if got != want {
				t.Fatalf("upstream values.region = %q, want the raw %q", got, want)
			}
		})
	}
}

// TestClusterDecisionResourceValuesDecisionFieldConflict covers a decision
// field named "values" in Go-template mode. Argo CD v3.6.1 panics in
// collectParams (applicationset/generators/duck_type.go:226) because the
// field is already a string. drydock fails the param set instead.
func TestClusterDecisionResourceValuesDecisionFieldConflict(t *testing.T) {
	generator := &argoappv1.DuckTypeGenerator{Values: map[string]string{"region": "east"}}
	clusters := map[string]ClusterInput{"alpha": {Name: "alpha", Server: "https://alpha.example.invalid"}}
	decision := map[string]any{"clusterName": "alpha", "values": "decision"}
	_, _, err := clusterDecisionResourceDecisionParamSet(generator, "clusterName", decision, clusters, true)
	if _, ok := errors.AsType[*generatorTemplateError](err); !ok {
		t.Fatalf("error = %v, want a generatorTemplateError", err)
	}
	// Without generator values, collectParams never runs and the decision
	// field stays a plain string param.
	paramSet, ok, err := clusterDecisionResourceDecisionParamSet(&argoappv1.DuckTypeGenerator{}, "clusterName", decision, clusters, true)
	if err != nil || !ok || paramSet.Params["values"] != "decision" {
		t.Fatalf("params = %#v, ok = %v, error = %v; want values = decision", paramSet.Params, ok, err)
	}
	// In fasttemplate mode the values keys are flat, so there is no conflict.
	paramSet, ok, err = clusterDecisionResourceDecisionParamSet(generator, "clusterName", decision, clusters, false)
	if err != nil || !ok || paramSet.Params["values"] != "decision" || paramSet.Params["values.region"] != "east" {
		t.Fatalf("fasttemplate params = %#v, ok = %v, error = %v", paramSet.Params, ok, err)
	}
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
