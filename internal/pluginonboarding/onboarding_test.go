package pluginonboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const digestImage = "registry.example.com/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGenerateContainerPolicyForNamedPlugin(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argocd", "repo-server.yaml"), repoServerDeployment("pkl", digestImage))
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		GenerateArgs:    []string{"eval", "."},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})

	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v\n%s", err, data)
	}
	text := string(data)
	for _, want := range []string{
		schemaModeline,
		`"pkl":`,
		`engine: container`,
		`image: "` + digestImage + `"`,
		`command: ["pkl", "eval", "."]`,
		`fileName: "PklProject"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated policy missing %q:\n%s", want, text)
		}
	}
	if _, err := pluginpolicy.Parse("generated.yaml", data); err != nil {
		t.Fatalf("Parse() error = %v\n%s", err, text)
	}
}

func TestGenerateUsesStaticDiscoveryForUnnamedPlugin(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "PklProject"), "package demo\n")
	writeFile(t, filepath.Join(root, "argocd", "repo-server.yaml"), repoServerDeployment("pkl", digestImage))
	app := sourceApp("argocd", "demo", "apps/demo")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		GenerateArgs:    []string{"eval", "."},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})

	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || !report.Plugins[0].Uses[0].StaticMatch {
		t.Fatalf("Plugins = %#v, want static match", report.Plugins)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(data), `fileName: "PklProject"`) {
		t.Fatalf("generated policy missing static discover:\n%s", data)
	}
}

func TestAnalyzeIgnoresTemplatedYAMLDuringSidecarScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "charts", "demo", "templates", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Values.name }}
`)
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})

	if _, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{}); err != nil {
		t.Fatalf("Analyze() error = %v, want templated YAML sidecar scan ignored", err)
	}
}

func TestAnalyzeDoesNotSelectAmbiguousStaticDiscovery(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "plugin.yaml"), "kind: ConfigMap\n")
	app := sourceApp("argocd", "demo", "apps/demo")
	settings := config.DefaultSettings()
	settings.ConfigManagementPlugins["alpha"] = config.ConfigManagementPlugin{
		Name:     "alpha",
		Discover: config.ConfigManagementPluginDiscovery{FileName: "plugin.yaml"},
	}
	settings.ConfigManagementPlugins["beta"] = config.ConfigManagementPlugin{
		Name:     "beta",
		Discover: config.ConfigManagementPluginDiscovery{FileName: "plugin.yaml"},
	}

	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 0 {
		t.Fatalf("Plugins = %#v, want no active scaffold for ambiguous static discovery", report.Plugins)
	}
}

func TestAnalyzeAcceptsGeneratedApplicationInput(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "generated", "apps/generated", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})

	report, err := Analyze(root, []ApplicationInput{{
		Application: app,
		Paths:       []string{"appsets/platform.yaml", "apps/generated"},
	}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || len(report.Plugins[0].Uses) != 1 || report.Plugins[0].Uses[0].AppName != "generated" {
		t.Fatalf("Plugins = %#v, want generated Application plugin evidence", report.Plugins)
	}
}

func TestGenerateAllowsObservedParametersAndEnv(t *testing.T) {
	root := t.TempDir()
	value := "values/prod.yaml"
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	app.Spec.Source.Plugin.Parameters = argoappv1.ApplicationSourcePluginParameters{
		{Name: "values-file", String_: &value},
		{Name: "items", OptionalArray: &argoappv1.OptionalArray{Array: []string{"one"}}},
	}
	app.Spec.Source.Plugin.Env = argoappv1.Env{{Name: "PKL_ENV", Value: "prod"}}
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})

	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`name: "items"`,
		`type: array`,
		`name: "values-file"`,
		`type: string`,
		`- "PKL_ENV"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated policy missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateTagOnlyImageUsesPlaceholderUnlessAllowed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argocd", "repo-server.yaml"), repoServerDeployment("pkl", "registry.example.com/plugins/pkl:v1.2.3"))
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, PlaceholderImage) || strings.Contains(text, "allowMutableImageTag: true") {
		t.Fatalf("tag-only default generated unexpected policy:\n%s", text)
	}
	data, err = Generate(report, GenerateOptions{Comments: true, AllowMutableImageTags: true})
	if err != nil {
		t.Fatalf("Generate(AllowMutableImageTags) error = %v", err)
	}
	text = string(data)
	for _, want := range []string{`image: "registry.example.com/plugins/pkl:v1.2.3"`, "allowMutableImageTag: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mutable-tag policy missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateAmbiguousSidecarUsesPlaceholder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argocd", "repo-server.yaml"), repoServerDeploymentWithSidecars(
		[]sidecarFixture{{name: "alpha", image: digestImage}, {name: "beta", image: "registry.example.com/plugins/beta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	))
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got := report.Plugins[0].Sidecar.Confidence; got != SidecarConfidenceAmbiguous {
		t.Fatalf("Sidecar confidence = %s, want ambiguous", got)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(data), PlaceholderImage) {
		t.Fatalf("ambiguous sidecar did not use placeholder:\n%s", data)
	}
}

func TestGenerateShellCommandUsesPlaceholder(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"sh"},
		GenerateArgs:    []string{"-c", "pkl eval ."},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(data), PlaceholderCommand) {
		t.Fatalf("shell command did not use placeholder:\n%s", data)
	}
	if strings.Contains(string(data), `configManagementPlugin:`) && strings.Contains(string(data), `command: ["sh"]`) {
		t.Fatalf("unsafe CMP seed generate command was emitted:\n%s", data)
	}
}

func TestGenerateInfersAVPCompatForAlias(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "avp-directory-include")
	settings := settingsWithCMP("avp-directory-include", config.ConfigManagementPlugin{
		Name:            "avp-directory-include",
		GenerateCommand: []string{"bash"},
		GenerateArgs:    []string{"-c", "argocd-vault-plugin generate ./"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `engine: avp-compat`) {
		t.Fatalf("generated policy did not infer avp-compat:\n%s", text)
	}
	for _, unwanted := range []string{PlaceholderImage, PlaceholderCommand, `copy:`, `generate:`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("generated avp-compat policy contains %q:\n%s", unwanted, text)
		}
	}
}

func TestGenerateExplicitEngineOverridesInferredEngine(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "avp-directory-include")
	settings := settingsWithCMP("avp-directory-include", config.ConfigManagementPlugin{
		Name:            "avp-directory-include",
		GenerateCommand: []string{"bash"},
		GenerateArgs:    []string{"-c", "argocd-vault-plugin generate ./"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Engine: pluginpolicy.EngineExec, EngineExplicit: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `engine: exec`) || strings.Contains(text, `engine: avp-compat`) {
		t.Fatalf("explicit engine was not respected:\n%s", text)
	}
}

func TestAnalyzeExtractsEmbeddedHelmValuesCMPForAVPCompat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "argocd-conf", "argocd-apps.yml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argo
spec:
  source:
    helm:
      valuesObject:
        repoServer:
          extraObjects:
            - apiVersion: v1
              kind: ConfigMap
              metadata:
                name: cmp-plugin
              data:
                avp-directory-include.yaml: |
                  apiVersion: argoproj.io/v1alpha1
                  kind: ConfigManagementPlugin
                  metadata:
                    name: avp-directory-include
                  spec:
                    generate:
                      command:
                        - bash
                        - "-c"
                      args:
                        - argocd-vault-plugin generate ./
`)
	app := pluginApp("argocd", "demo", "apps/demo", "avp-directory-include")
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, config.DefaultSettings(), nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"avp-directory-include":`) || !strings.Contains(text, `engine: avp-compat`) {
		t.Fatalf("generated policy did not use embedded AVP CMP evidence:\n%s", text)
	}
}

func TestGenerateInfersNativeKustomize(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "kustomize-build-with-helm")
	settings := settingsWithCMP("kustomize-build-with-helm", config.ConfigManagementPlugin{
		Name:            "kustomize-build-with-helm",
		GenerateCommand: []string{"sh", "-c"},
		GenerateArgs:    []string{"kustomize build --enable-helm"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "kustomization.yaml"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{`engine: native-kustomize`, `fileName: "kustomization.yaml"`, `command: ["kustomize", "build"]`, `args: ["--enable-helm"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated policy missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{PlaceholderImage, PlaceholderCommand, `copy:`, `command: ["sh", "-c"]`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("generated native-kustomize policy contains %q:\n%s", unwanted, text)
		}
	}
}

func TestGenerateInfersNativeKustomizeWithPluginFlags(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "kustomize-build-ksops")
	settings := settingsWithCMP("kustomize-build-ksops", config.ConfigManagementPlugin{
		Name:            "kustomize-build-ksops",
		GenerateCommand: []string{"sh", "-c"},
		GenerateArgs:    []string{"kustomize build --enable-helm --enable-alpha-plugins --enable-exec"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "kustomization.yaml"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{`engine: native-kustomize`, `args: ["--enable-helm", "--enable-alpha-plugins", "--enable-exec"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated policy missing %q:\n%s", want, text)
		}
	}
}

func TestAnalyzeUsesEmbeddedCMPForUnnamedStaticDiscovery(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources: []\n")
	writeFile(t, filepath.Join(root, "argocd", "values.yaml"), `repoServer:
  extraObjects:
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: cmp-plugin
      data:
        kustomize-build-with-helm.yaml: |
          apiVersion: argoproj.io/v1alpha1
          kind: ConfigManagementPlugin
          metadata:
            name: kustomize-build-with-helm
          spec:
            discover:
              fileName: kustomization.yaml
            generate:
              command: [sh, -c]
              args: [kustomize build --enable-helm]
`)
	app := sourceApp("argocd", "demo", "apps/demo")
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, config.DefaultSettings(), nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].Name != "kustomize-build-with-helm" || len(report.Plugins[0].Uses) != 1 || !report.Plugins[0].Uses[0].StaticMatch {
		t.Fatalf("Plugins = %#v, want embedded CMP static match", report.Plugins)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{`engine: native-kustomize`, `command: ["kustomize", "build"]`, `args: ["--enable-helm"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated policy missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateBootstrapEntrypoint(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "bootstrap", "PklProject"), "package bootstrap\n")
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{
		BootstrapEntrypoints: []BootstrapEntrypointHint{{Plugin: "pkl", SourcePath: "apps/bootstrap"}},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, want := range []string{`bootstrap:`, `plugin: "pkl"`, `sourcePath: "apps/bootstrap"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("bootstrap policy missing %q:\n%s", want, data)
		}
	}
}

func TestAnalyzeRecordsMultiSourcePluginUsage(t *testing.T) {
	root := t.TempDir()
	app := sourceApp("argocd", "demo", "apps/plain")
	app.Spec.Source = nil
	app.Spec.Sources = argoappv1.ApplicationSources{
		{RepoURL: "https://example.com/repo.git", Path: "apps/plain"},
		{RepoURL: "https://example.com/repo.git", Path: "apps/plugin", Plugin: &argoappv1.ApplicationSourcePlugin{Name: "pkl"}},
	}
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})

	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || len(report.Plugins[0].Uses) != 1 {
		t.Fatalf("Plugins = %#v, want one plugin use", report.Plugins)
	}
	use := report.Plugins[0].Uses[0]
	if use.SourceIndex != 1 || use.SourcePath != "apps/plugin" || !use.Explicit {
		t.Fatalf("Plugin use = %#v, want multi-source index 1 explicit use", use)
	}
}

func TestAnalyzeIncludeUnusedAddsCMPDescriptor(t *testing.T) {
	root := t.TempDir()
	settings := settingsWithCMP("unused", config.ConfigManagementPlugin{
		Name:            "unused",
		GenerateCommand: []string{"unused-render"},
		Discover:        config.ConfigManagementPluginDiscovery{FindGlob: "**/plugin.yaml"},
	})

	report, err := Analyze(root, nil, settings, nil, AnalyzeOptions{IncludeUnused: true})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].Name != "unused" || report.Plugins[0].Discover == nil {
		t.Fatalf("Plugins = %#v, want unused CMP descriptor", report.Plugins)
	}
}

func TestAnalyzeSeedsExistingPolicyPluginsAndBootstrap(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "personal-cluster", "PklProject"), "package cluster\n")
	policy, err := pluginpolicy.Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: personal-cluster
      plugin: pkl
      sourcePath: personal-cluster
plugins:
  pkl:
    engine: container
    image: registry.example.com/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    configManagementPlugin:
      discover:
        fileName: PklProject
    copy:
      scope: source
    generate:
      command: ["pkl", "eval", "."]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	report, err := Analyze(root, nil, config.DefaultSettings(), &policy, AnalyzeOptions{
		BootstrapEntrypoints: []BootstrapEntrypointHint{{Plugin: "pkl", SourcePath: "personal-cluster"}},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].Name != "pkl" || report.Plugins[0].Discover == nil {
		t.Fatalf("Plugins = %#v, want existing policy pkl with discover", report.Plugins)
	}
	data, err := Generate(report, GenerateOptions{Comments: false})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, want := range []string{`bootstrap:`, `plugin: "pkl"`, `fileName: "PklProject"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated policy missing %q:\n%s", want, data)
		}
	}
}

func TestGenerateExecPolicy(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}, GenerateArgs: []string{"eval", "."}})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Engine: pluginpolicy.EngineExec})
	if err != nil {
		t.Fatalf("Generate(exec) error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "image:") || !strings.Contains(text, "engine: exec") || !strings.Contains(text, `command: ["pkl", "eval", "."]`) {
		t.Fatalf("exec policy has unexpected content:\n%s", text)
	}
}

func TestReadinessDetectsMissingPolicy(t *testing.T) {
	report := Report{Plugins: []PluginReport{{Name: "pkl", Used: true}}}
	readiness := Readiness(report, nil, DoctorOptions{})
	if readiness.Status != StatusFail || !hasIssue(readiness.Recommendations, IssuePolicyMissing) {
		t.Fatalf("Readiness = %#v, want missing policy failure", readiness)
	}
}

func TestGenerateBootstrapEntrypointRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "PklProject"), "package bootstrap\n")
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Skipf("Symlink() not supported: %v", err)
	}
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{
		BootstrapEntrypoints: []BootstrapEntrypointHint{{Plugin: "pkl", SourcePath: "linked"}},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if _, err := Generate(report, GenerateOptions{Comments: true}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Generate() error = %v, want symlink rejection", err)
	}
}

func TestGenerateBootstrapEntrypointRejectsMissingPath(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		Discover:        config.ConfigManagementPluginDiscovery{FileName: "PklProject"},
	})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{
		BootstrapEntrypoints: []BootstrapEntrypointHint{{Plugin: "pkl", SourcePath: "apps/missing"}},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if _, err := Generate(report, GenerateOptions{Comments: true}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("Generate() error = %v, want missing path rejection", err)
	}
}

func TestReadinessDetectsPlaceholdersAndMutableImages(t *testing.T) {
	root := t.TempDir()
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err := Generate(report, GenerateOptions{Comments: true})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	policy, err := pluginpolicy.Parse("generated.yaml", data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	readiness := Readiness(report, &policy, DoctorOptions{EnablePlugins: true, TrustedPolicy: true})
	if readiness.Status != StatusFail || !hasIssue(readiness.Plugins[0].Issues, IssueImagePlaceholder) {
		t.Fatalf("Readiness = %#v, want placeholder failure", readiness)
	}

	writeFile(t, filepath.Join(root, "argocd", "repo-server.yaml"), repoServerDeployment("pkl", "registry.example.com/plugins/pkl:v1.2.3"))
	report, err = Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	data, err = Generate(report, GenerateOptions{Comments: true, AllowMutableImageTags: true})
	if err != nil {
		t.Fatalf("Generate mutable() error = %v", err)
	}
	policy, err = pluginpolicy.Parse("generated.yaml", data)
	if err != nil {
		t.Fatalf("Parse mutable() error = %v", err)
	}
	readiness = Readiness(report, &policy, DoctorOptions{EnablePlugins: true, TrustedPolicy: true})
	if readiness.Status != StatusWarn || !hasIssue(readiness.Plugins[0].Issues, IssueImageMutable) {
		t.Fatalf("Readiness = %#v, want mutable warning", readiness)
	}
	readiness = Readiness(report, &policy, DoctorOptions{EnablePlugins: true, TrustedPolicy: true, Strict: true})
	if readiness.Status != StatusFail || !hasIssue(readiness.Plugins[0].Issues, IssueImageMutable) {
		t.Fatalf("Strict readiness = %#v, want mutable failure", readiness)
	}
}

func TestReadinessDetectsMissingParameterAndEnvAllows(t *testing.T) {
	root := t.TempDir()
	value := "prod"
	app := pluginApp("argocd", "demo", "apps/demo", "pkl")
	app.Spec.Source.Plugin.Parameters = argoappv1.ApplicationSourcePluginParameters{{Name: "cluster", String_: &value}}
	app.Spec.Source.Plugin.Env = argoappv1.Env{{Name: "PKL_ENV", Value: "prod"}}
	settings := settingsWithCMP("pkl", config.ConfigManagementPlugin{Name: "pkl", GenerateCommand: []string{"pkl"}})
	report, err := Analyze(root, []ApplicationInput{{Application: app}}, settings, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	policy, err := pluginpolicy.Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: exec
    generate:
      command: ["pkl"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	readiness := Readiness(report, &policy, DoctorOptions{EnablePlugins: true, TrustedPolicy: true})
	issues := readiness.Plugins[0].Issues
	if !hasIssue(issues, IssueParamsMissingAllow) || !hasIssue(issues, IssueEnvMissingAllow) {
		t.Fatalf("Readiness issues = %#v, want params/env missing allow", issues)
	}
}

func pluginApp(namespace, name, sourcePath, plugin string) argoappv1.Application {
	app := sourceApp(namespace, name, sourcePath)
	app.Spec.Source.Plugin = &argoappv1.ApplicationSourcePlugin{Name: plugin}
	return app
}

func sourceApp(namespace, name, sourcePath string) argoappv1.Application {
	return argoappv1.Application{
		Namespace: namespace, Name: name,
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://example.com/repo.git",
				Path:    sourcePath,
			},
		},
	}
}

func settingsWithCMP(name string, plugin config.ConfigManagementPlugin) config.ArgoSettings {
	settings := config.DefaultSettings()
	settings.ConfigManagementPlugins[name] = plugin
	return settings
}

type sidecarFixture struct {
	name  string
	image string
}

func repoServerDeployment(name, image string) string {
	return repoServerDeploymentWithSidecars([]sidecarFixture{{name: name, image: image}})
}

func repoServerDeploymentWithSidecars(sidecars []sidecarFixture) string {
	var b strings.Builder
	b.WriteString(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
spec:
  template:
    spec:
      containers:
        - name: argocd-repo-server
          image: quay.io/argoproj/argocd:v3.0.0
`)
	for _, sidecar := range sidecars {
		b.WriteString("        - name: " + sidecar.name + "\n")
		b.WriteString("          image: " + sidecar.image + "\n")
		b.WriteString("          volumeMounts:\n")
		b.WriteString("            - name: argocd-cmp-cm\n")
		b.WriteString("              subPath: " + sidecar.name + ".yaml\n")
	}
	return b.String()
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func hasIssue(issues []ReadinessIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
