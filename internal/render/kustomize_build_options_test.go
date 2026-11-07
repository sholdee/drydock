package render

import (
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kustomize/v5/commands"
	"sigs.k8s.io/kustomize/kustomize/v5/commands/build"
)

// parseKustomizeBuildOptions parses options as the kustomize.buildOptions
// of a found argocd-cm, for tests.
func parseKustomizeBuildOptions(options []string) (kustomizeBuildSettings, error) {
	return kustomizeBuildSettingsFor(RenderOptions{KustomizeCaller: KustomizeCallerArgoCD, BuildOptions: options})
}

// upstreamKustomizeBuild parses `kustomize build <args>` with the build
// command of the kustomize v5.8.3 binary, as cobra runs it: the root
// command and its persistent flags, the --help flag, the flag parse, the
// help check, build.Validate, and build.HonorKustomizeFlags. It returns the
// krusty options and the --output value, or the error that kustomize
// prints after "Error: ". help reports that kustomize prints its help.
func upstreamKustomizeBuild(t *testing.T, args []string) (options *krusty.Options, output string, help bool, err error) {
	t.Helper()
	root := commands.NewDefaultCommand()
	cmd, _, err := root.Find([]string{"build"})
	if err != nil || cmd.Name() != "build" {
		t.Fatalf("kustomize root has no build command: %v", err)
	}
	cmd.InitDefaultHelpFlag()
	if err := cmd.ParseFlags(args); err != nil {
		return nil, "", false, err
	}
	if help, _ := cmd.Flags().GetBool("help"); help {
		return nil, "", true, nil
	}
	if err := build.Validate(cmd.Flags().Args()); err != nil {
		return nil, "", false, err
	}
	output, _ = cmd.Flags().GetString("output")
	return build.HonorKustomizeFlags(krusty.MakeDefaultOptions(), cmd.Flags()), output, false, nil
}

// The build flags that drydock reads are the ones that the kustomize v5.8.3
// binary reads, and they mean the same. Each case is kustomize.buildOptions,
// after the source path that Argo CD passes first.
func TestKustomizeBuildFlagsMatchKustomizeBinary(t *testing.T) {
	t.Setenv("KUSTOMIZE_ENABLE_MANAGEDBY_LABEL", "")
	for _, tc := range []struct {
		name    string
		options []string
		// failClosed: kustomize accepts the options, and drydock fails,
		// because their output is not the build.
		failClosed string
	}{
		{name: "no options"},
		{name: "enable helm", options: []string{"--enable-helm"}},
		{name: "enable helm true", options: []string{"--enable-helm=true"}},
		{name: "enable helm false", options: []string{"--enable-helm=false"}},
		{name: "enable helm 1", options: []string{"--enable-helm=1"}},
		{name: "enable helm F", options: []string{"--enable-helm=F"}},
		{name: "enable helm not a bool", options: []string{"--enable-helm=maybe"}},
		{name: "enable helm separate value", options: []string{"--enable-helm", "true"}},
		{name: "alpha plugins", options: []string{"--enable-alpha-plugins"}},
		{name: "alpha plugins and exec", options: []string{"--enable-alpha-plugins", "--enable-exec"}},
		{name: "exec only", options: []string{"--enable-exec"}},
		{name: "reorder none", options: []string{"--reorder", "none"}},
		{name: "reorder legacy", options: []string{"--reorder=legacy"}},
		{name: "reorder illegal", options: []string{"--reorder", "bad"}},
		{name: "managedby label", options: []string{"--enable-managedby-label"}},
		{name: "empty load restrictor", options: []string{"--load-restrictor="}},
		{name: "load restrictor none", options: []string{"--load-restrictor", "LoadRestrictionsNone"}},
		{name: "load restrictor short none", options: []string{"--load-restrictor=none"}},
		{name: "load restrictor illegal", options: []string{"--load-restrictor", "x"}},
		{name: "load restrictor then illegal reorder", options: []string{"--load-restrictor=x", "--reorder=y"}},
		{name: "helm kube version", options: []string{"--helm-kube-version", "1.29.0"}},
		{name: "helm kube version last wins", options: []string{"--helm-kube-version=1.29.0", "--helm-kube-version", "1.30.0"}},
		{name: "helm api versions", options: []string{"--helm-api-versions", "a.example/v1,b.example/v1", "--helm-api-versions=c.example/v1"}},
		{name: "helm command", options: []string{"--helm-command", "helm3"}},
		{name: "empty helm command", options: []string{"--helm-command="}},
		{name: "helm command without value", options: []string{"--helm-command"}},
		{name: "helm debug", options: []string{"--helm-debug"}},
		{name: "function flags", options: []string{"--network", "--network-name", "host", "-e", "A=1", "-eB=2", "--env=C=3", "--mount", "type=bind,src=/a,dst=/b", "--as-current-user", "--stack-trace"}},
		{name: "unknown flag", options: []string{"--no-such-flag"}},
		{name: "unknown shorthand", options: []string{"-x"}},
		{name: "second path", options: []string{"other"}},
		{name: "flags after double dash", options: []string{"--", "--enable-helm"}},
		{name: "help", options: []string{"--help"}, failClosed: "--help"},
		{name: "help shorthand", options: []string{"-h"}, failClosed: "--help"},
		{name: "output", options: []string{"-o", "out.yaml"}, failClosed: "--output"},
		{name: "output long", options: []string{"--output=out.yaml"}, failClosed: "--output"},
		{name: "empty output", options: []string{"--output="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"."}, tc.options...)
			want, output, help, wantErr := upstreamKustomizeBuild(t, args)
			got, gotErr := parseKustomizeBuildOptions(tc.options)
			if tc.failClosed != "" {
				if wantErr != nil || (!help && output == "") {
					t.Fatalf("kustomize = %v (help %t, output %q), want a build that drydock fails closed", wantErr, help, output)
				}
				if gotErr == nil || !strings.Contains(gotErr.Error(), tc.failClosed) {
					t.Fatalf("drydock error = %v, want it to name %s", gotErr, tc.failClosed)
				}
				return
			}
			if wantErr != nil {
				if gotErr == nil || gotErr.Error() != "kustomize build: "+wantErr.Error() {
					t.Fatalf("drydock error = %v, want kustomize build: %v", gotErr, wantErr)
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("drydock error = %v, want none as kustomize", gotErr)
			}
			assertKustomizeBuildSettingsMatch(t, got, want)
		})
	}
}

func assertKustomizeBuildSettingsMatch(t *testing.T, got kustomizeBuildSettings, want *krusty.Options) {
	t.Helper()
	helm := want.PluginConfig.HelmConfig
	wantAPIVersions := helm.ApiVersions
	if wantAPIVersions == nil {
		wantAPIVersions = []string{}
	}
	for _, check := range []struct {
		field     string
		got, want any
	}{
		{"LoadRestrictions", got.LoadRestrictions, want.LoadRestrictions},
		{"Reorder", got.Reorder, want.Reorder},
		{"AddManagedbyLabel", got.AddManagedbyLabel, want.AddManagedbyLabel},
		{"HelmEnabled", got.HelmEnabled, helm.Enabled},
		{"HelmCommand", got.HelmCommand, helm.Command},
		{"HelmKubeVersion", got.HelmKubeVersion, helm.KubeVersion},
		{"HelmAPIVersions", got.HelmAPIVersions, wantAPIVersions},
		{"EnableAlphaPlugins", got.EnableAlphaPlugins, want.PluginConfig.PluginRestrictions == types.PluginRestrictionsNone},
		// kustomize keeps --enable-exec only with alpha plugins on.
		{"EnableExec", got.EnableExec && got.EnableAlphaPlugins, want.PluginConfig.FnpLoadingOptions.EnableExec},
	} {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s = %#v, kustomize %#v", check.field, check.got, check.want)
		}
	}
	// kustomize shares the backing array of the flag values with the
	// helmCharts entries, so its capacity must match too.
	if cap(got.HelmAPIVersions) != cap(helm.ApiVersions) {
		t.Errorf("cap(HelmAPIVersions) = %d, kustomize %d", cap(got.HelmAPIVersions), cap(helm.ApiVersions))
	}
}

// splitKustomizeBuildOperands finds the operands that pflag finds.
func TestSplitKustomizeBuildOperandsMatchesPflag(t *testing.T) {
	for _, words := range [][]string{
		{},
		{"."},
		{".", "--enable-helm"},
		{"--enable-helm", "."},
		{"--load-restrictor", ".", "."},
		{"--load-restrictor=.", "."},
		{"--helm-api-versions", "a/v1", ".", "--helm-kube-version", "1.29.0"},
		{"-e", "A=1", "."},
		{"-eA=1", "."},
		{"-he", ".", "x"},
		{"-heA", "."},
		{"-", "."},
		{"--", "--enable-helm", "."},
		{".", "--network", "x"},
	} {
		var values kustomizeBuildFlagValues
		flags := newKustomizeBuildFlagSet(&values)
		if err := flags.Parse(words); err != nil {
			t.Fatalf("Parse(%q) error = %v", words, err)
		}
		options, operands := splitKustomizeBuildOperands(newKustomizeBuildFlagSet(&kustomizeBuildFlagValues{}), words)
		if !slices.Equal(operands, flags.Args()) && (len(operands) != 0 || len(flags.Args()) != 0) {
			t.Errorf("operands of %q = %q, pflag %q", words, operands, flags.Args())
		}
		// The options without the operands parse to the same values.
		var again kustomizeBuildFlagValues
		if err := newKustomizeBuildFlagSet(&again).Parse(options); err != nil {
			t.Fatalf("Parse(%q) error = %v", options, err)
		}
		if !reflect.DeepEqual(again, values) {
			t.Errorf("options %q of %q parse to %#v, want %#v", options, words, again, values)
		}
	}
}

// The words that the repo-server passes to kustomize build after the
// source path (parseKustomizeBuildOptions, util/kustomize/kustomize.go:
// 432-449 at v3.6.1): kustomize.buildOptions split on white space, then,
// when the text contains "--enable-helm", the kube version and one
// --helm-api-versions per API version of the Application.
func TestKustomizeBuildArgsAddArgoCDHelmFlags(t *testing.T) {
	application := RenderOptions{KubeVersion: "v1.31.0", APIVersions: []string{"a.example/v1", "b.example/v1"}}
	for _, tc := range []struct {
		name    string
		caller  KustomizeBuildCaller
		options []string
		app     RenderOptions
		want    []string
	}{
		{name: "no argocd-cm", caller: KustomizeCallerArgoCDAssumed, app: application,
			want: []string{"--enable-helm", "--helm-kube-version", "v1.31.0", "--helm-api-versions", "a.example/v1", "--helm-api-versions", "b.example/v1"}},
		{name: "argocd-cm without options", caller: KustomizeCallerArgoCD, app: application, want: []string{}},
		{name: "enable helm", caller: KustomizeCallerArgoCD, options: []string{"--load-restrictor", "LoadRestrictionsNone", "--enable-helm"}, app: application,
			want: []string{"--load-restrictor", "LoadRestrictionsNone", "--enable-helm", "--helm-kube-version", "v1.31.0", "--helm-api-versions", "a.example/v1", "--helm-api-versions", "b.example/v1"}},
		// isHelmEnabled is a substring check, so a false value still adds
		// the flags.
		{name: "enable helm false", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm=false"}, app: application,
			want: []string{"--enable-helm=false", "--helm-kube-version", "v1.31.0", "--helm-api-versions", "a.example/v1", "--helm-api-versions", "b.example/v1"}},
		// Alpha plugins turn Helm on in kustomize, but Argo CD adds
		// nothing (Audit 15).
		{name: "alpha plugins only", caller: KustomizeCallerArgoCD, options: []string{"--enable-alpha-plugins", "--helm-kube-version", "1.29.0"}, app: application,
			want: []string{"--enable-alpha-plugins", "--helm-kube-version", "1.29.0"}},
		{name: "no kube version", caller: KustomizeCallerArgoCD, options: []string{"--enable-helm"}, app: RenderOptions{APIVersions: []string{"a.example/v1"}},
			want: []string{"--enable-helm", "--helm-api-versions", "a.example/v1"}},
		{name: "plugin command", caller: KustomizeCallerCommand, options: []string{"--enable-helm"}, app: application, want: []string{"--enable-helm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.app
			opts.KustomizeCaller = tc.caller
			opts.BuildOptions = tc.options
			if got := kustomizeBuildArgs(opts); !slices.Equal(got, tc.want) {
				t.Fatalf("kustomizeBuildArgs() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Argo CD appends its --helm-kube-version after kustomize.buildOptions, so
// with --enable-helm the kube version of the Application wins over one in
// the options, while the API versions add up.
func TestKustomizeBuildSettingsArgoCDFlagsComeLast(t *testing.T) {
	settings, err := kustomizeBuildSettingsFor(RenderOptions{
		KustomizeCaller: KustomizeCallerArgoCD,
		BuildOptions:    []string{"--enable-helm", "--helm-kube-version", "1.29.0", "--helm-api-versions", "options.example/v1"},
		KubeVersion:     "v1.31.0",
		APIVersions:     []string{"app.example/v1"},
	})
	if err != nil {
		t.Fatalf("kustomizeBuildSettingsFor() error = %v", err)
	}
	if settings.HelmKubeVersion != "v1.31.0" {
		t.Fatalf("HelmKubeVersion = %q, want the Application kube version", settings.HelmKubeVersion)
	}
	if want := []string{"options.example/v1", "app.example/v1"}; !slices.Equal(settings.HelmAPIVersions, want) {
		t.Fatalf("HelmAPIVersions = %q, want %q", settings.HelmAPIVersions, want)
	}
}

// argoCDKustomizeVersion names the kustomize binary whose flags and
// behavior the tests compare with. It must follow go.mod.
func TestArgoCDKustomizeVersion(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information")
	}
	for _, module := range info.Deps {
		if module.Path == "sigs.k8s.io/kustomize/kustomize/v5" {
			if module.Version != argoCDKustomizeVersion {
				t.Fatalf("go.mod has sigs.k8s.io/kustomize/kustomize/v5 %s, argoCDKustomizeVersion is %s", module.Version, argoCDKustomizeVersion)
			}
			return
		}
	}
	t.Fatal("sigs.k8s.io/kustomize/kustomize/v5 is not in the build information of the test")
}

// pflag must be the version that kustomize v5.8.3 builds with, so that the
// flag values and their slice capacity match.
var _ = pflag.ContinueOnError
