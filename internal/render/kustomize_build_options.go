package render

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
)

// KustomizeBuildCaller says who runs `kustomize build` for a source, so
// which flags the build gets.
type KustomizeBuildCaller int

const (
	// KustomizeCallerArgoCDAssumed is the Argo CD repo-server when drydock
	// found no argocd-cm. drydock then reads kustomize.buildOptions as
	// "--enable-helm" followed by BuildOptions, so a repository without its
	// Argo CD settings keeps rendering helmCharts.
	KustomizeCallerArgoCDAssumed KustomizeBuildCaller = iota
	// KustomizeCallerArgoCD is the Argo CD repo-server with the
	// kustomize.buildOptions of a found argocd-cm in BuildOptions.
	KustomizeCallerArgoCD
	// KustomizeCallerCommand is a config management plugin whose generate
	// command is a plain `kustomize build`. BuildOptions hold the words of
	// that command after "build", and nothing is added to them.
	KustomizeCallerCommand
)

// argoCDKustomizeVersion is the version of the kustomize binary in the Argo
// CD v3.6.1 image (hack/tool-versions.sh), which the
// app.kubernetes.io/managed-by label names. TestArgoCDKustomizeVersion keeps
// it equal to the version of sigs.k8s.io/kustomize/kustomize/v5 in go.mod.
const argoCDKustomizeVersion = "v5.8.3"

// kustomizeBuildSettings holds the flags of one `kustomize build` run, as
// HonorKustomizeFlags passes them to krusty (kustomize/kustomize/v5
// commands/build/build.go at v5.8.3).
type kustomizeBuildSettings struct {
	LoadRestrictions  types.LoadRestrictions
	Reorder           krusty.ReorderOption
	AddManagedbyLabel bool
	// HelmEnabled is the HelmConfig.Enabled that kustomize checks before it
	// configures a helmCharts entry: --enable-helm, or
	// --enable-alpha-plugins, which turns every plugin on, Helm included.
	HelmEnabled bool
	// HelmCommand is --helm-command. drydock renders helmCharts with the
	// Helm library, so it accepts only the default, "helm".
	HelmCommand string
	// HelmKubeVersion and HelmAPIVersions are --helm-kube-version and
	// --helm-api-versions. HelmAPIVersions is the slice that pflag built,
	// with its capacity, because kustomize shares its backing array with
	// the helmCharts entries (see kustomizeHelmFlagState).
	HelmKubeVersion string
	HelmAPIVersions []string
	// EnableAlphaPlugins and EnableExec are read but enable no execution:
	// drydock runs no plugin program. Generator entries are emulated
	// (KSOPS under --enable-ksops-compat), left to the builtins of krusty,
	// or rejected with a diagnostic.
	EnableAlphaPlugins bool
	EnableExec         bool
}

// kustomizeBuildFlagValues are the targets of the kustomize build flag set.
type kustomizeBuildFlagValues struct {
	outputPath      string
	network         bool
	networkName     string
	mounts          []string
	env             []string
	asCurrentUser   bool
	loadRestrictor  string
	enablePlugins   bool
	reorder         string
	managedByLabel  bool
	enableHelm      bool
	helmCommand     string
	helmAPIVersions []string
	helmKubeVersion string
	helmDebug       bool
	enableExec      bool
	help            bool
	stackTrace      bool
}

// newKustomizeBuildFlagSet returns the flag set of `kustomize build` in the
// kustomize v5.8.3 binary: the flags of NewCmdBuild in their order, the
// --enable-exec flag that makeBuildCommand adds
// (kustomize/v5 commands/commands.go), the --help flag that cobra adds, and
// the persistent --stack-trace flag of the root command. Flags that only
// change what kustomize logs or how it runs a KRM function have no effect
// here: --helm-debug, --stack-trace, --network, --network-name, --mount,
// --env, and --as-current-user. drydock runs no function.
func newKustomizeBuildFlagSet(values *kustomizeBuildFlagValues) *pflag.FlagSet {
	flags := pflag.NewFlagSet("build", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	flags.StringVarP(&values.outputPath, "output", "o", "", "")
	flags.BoolVar(&values.network, "network", false, "")
	flags.StringVar(&values.networkName, "network-name", "bridge", "")
	flags.StringArrayVar(&values.mounts, "mount", []string{}, "")
	flags.StringArrayVarP(&values.env, "env", "e", []string{}, "")
	flags.BoolVar(&values.asCurrentUser, "as-current-user", false, "")
	flags.StringVar(&values.loadRestrictor, "load-restrictor", types.LoadRestrictionsRootOnly.String(), "")
	flags.BoolVar(&values.enablePlugins, "enable-alpha-plugins", false, "")
	flags.StringVar(&values.reorder, "reorder", string(krusty.ReorderOptionLegacy), "")
	flags.BoolVar(&values.managedByLabel, "enable-managedby-label", false, "")
	flags.BoolVar(&values.enableHelm, "enable-helm", false, "")
	flags.StringVar(&values.helmCommand, "helm-command", "helm", "")
	flags.StringArrayVar(&values.helmAPIVersions, "helm-api-versions", []string{}, "")
	flags.StringVar(&values.helmKubeVersion, "helm-kube-version", "", "")
	flags.BoolVar(&values.helmDebug, "helm-debug", false, "")
	flags.BoolVar(&values.enableExec, "enable-exec", false, "")
	flags.BoolVarP(&values.help, "help", "h", false, "")
	flags.BoolVar(&values.stackTrace, "stack-trace", false, "")
	return flags
}

// kustomizeBuildError is an error that kustomize build reports before it
// builds. kustomize prints it as "Error: <text>", and Argo CD adds that
// line to its own message.
func kustomizeBuildError(format string, args ...any) error {
	return fmt.Errorf("kustomize build: "+format, args...)
}

// parseKustomizeBuildArgs parses the words after `kustomize build` as the
// kustomize v5.8.3 binary does: pflag parses the flags, then
// build.Validate checks the operands, --load-restrictor, and --reorder, in
// that order. It returns the settings and the operands.
func parseKustomizeBuildArgs(args []string) (kustomizeBuildSettings, []string, error) {
	var values kustomizeBuildFlagValues
	flags := newKustomizeBuildFlagSet(&values)
	if err := flags.Parse(args); err != nil {
		return kustomizeBuildSettings{}, nil, kustomizeBuildError("%s", err.Error())
	}
	if values.help {
		return kustomizeBuildSettings{}, nil, fmt.Errorf("kustomize build option --help prints the help text instead of manifests; drydock does not render it")
	}
	operands := flags.Args()
	if len(operands) > 1 {
		return kustomizeBuildSettings{}, operands, kustomizeBuildError("specify one path to %s", kustomizationFileNames[0])
	}
	switch values.loadRestrictor {
	case types.LoadRestrictionsRootOnly.String(), types.LoadRestrictionsNone.String(), "":
	default:
		return kustomizeBuildSettings{}, operands, kustomizeBuildError("illegal flag value --load-restrictor %s; legal values: %v", values.loadRestrictor,
			[]string{types.LoadRestrictionsRootOnly.String(), types.LoadRestrictionsNone.String()})
	}
	switch values.reorder {
	case string(krusty.ReorderOptionNone), string(krusty.ReorderOptionLegacy):
	default:
		return kustomizeBuildSettings{}, operands, kustomizeBuildError("illegal flag value --reorder %s; legal values: %v", values.reorder,
			[]string{string(krusty.ReorderOptionLegacy), string(krusty.ReorderOptionNone)})
	}
	if values.outputPath != "" {
		return kustomizeBuildSettings{}, operands, fmt.Errorf("kustomize build option --output writes the build to %q instead of its output, so Argo CD reads no manifests; drydock does not render it", values.outputPath)
	}
	settings := kustomizeBuildSettings{
		LoadRestrictions:   types.LoadRestrictionsRootOnly,
		Reorder:            krusty.ReorderOptionUnspecified,
		AddManagedbyLabel:  values.managedByLabel,
		HelmEnabled:        values.enableHelm || values.enablePlugins,
		HelmCommand:        values.helmCommand,
		HelmKubeVersion:    values.helmKubeVersion,
		HelmAPIVersions:    values.helmAPIVersions,
		EnableAlphaPlugins: values.enablePlugins,
		EnableExec:         values.enableExec,
	}
	if values.loadRestrictor == types.LoadRestrictionsNone.String() {
		settings.LoadRestrictions = types.LoadRestrictionsNone
	}
	// getFlagReorderOutput: the default applies only when the flag is not
	// set, and krusty then sorts in the legacy order.
	if flags.Changed("reorder") {
		settings.Reorder = krusty.ReorderOption(values.reorder)
	}
	return settings, operands, nil
}

// kustomizeBuildArgs returns the words after `kustomize build <path>` that
// the build of a source gets. The Argo CD repo-server splits
// kustomize.buildOptions on white space, with no quoting, and adds
// --helm-kube-version and one --helm-api-versions per API version after
// them when the text contains "--enable-helm" anywhere
// (parseKustomizeBuildOptions and isHelmEnabled,
// util/kustomize/kustomize.go:432-449 at v3.6.1). The kube version and API
// versions are the ones of the Application, from opts.
func kustomizeBuildArgs(opts RenderOptions) []string {
	if opts.KustomizeCaller == KustomizeCallerCommand {
		return append([]string(nil), opts.BuildOptions...)
	}
	args := make([]string, 0, len(opts.BuildOptions)+1)
	if opts.KustomizeCaller == KustomizeCallerArgoCDAssumed {
		args = append(args, "--enable-helm")
	}
	args = append(args, opts.BuildOptions...)
	if !strings.Contains(strings.Join(args, " "), "--enable-helm") {
		return args
	}
	if opts.KubeVersion != "" {
		args = append(args, "--helm-kube-version", opts.KubeVersion)
	}
	for _, apiVersion := range opts.APIVersions {
		args = append(args, "--helm-api-versions", apiVersion)
	}
	return args
}

// kustomizeBuildSettingsFor parses the build flags of a source. The Argo
// CD repo-server passes the source path first, so an operand in
// kustomize.buildOptions is a second path. A plugin command may name only
// the source directory, ".".
func kustomizeBuildSettingsFor(opts RenderOptions) (kustomizeBuildSettings, error) {
	args := kustomizeBuildArgs(opts)
	if opts.KustomizeCaller != KustomizeCallerCommand {
		args = append([]string{"."}, args...)
	}
	settings, operands, err := parseKustomizeBuildArgs(args)
	if err != nil {
		return settings, err
	}
	if opts.KustomizeCaller == KustomizeCallerCommand {
		for _, operand := range operands {
			if operand != "." {
				return settings, fmt.Errorf("unsupported kustomize build path or remote operand")
			}
		}
	}
	return settings, nil
}

// ValidateKustomizeBuildOptions reports whether kustomize build accepts
// options as the kustomize.buildOptions of Argo CD.
func ValidateKustomizeBuildOptions(options []string) error {
	_, err := kustomizeBuildSettingsFor(RenderOptions{KustomizeCaller: KustomizeCallerArgoCD, BuildOptions: options})
	return err
}

// NormalizeKustomizeBuildTokens checks that tokens form a plain
// "kustomize build" command for the source directory and returns the
// words after "build" without the "." operand. A flag that kustomize
// rejects, or that drydock does not render, fails, and so does any other
// path or remote operand.
func NormalizeKustomizeBuildTokens(tokens []string) ([]string, error) {
	if len(tokens) < 2 || filepath.Base(tokens[0]) != "kustomize" || tokens[1] != "build" {
		return nil, fmt.Errorf("generate command is not kustomize build")
	}
	words := make([]string, 0, len(tokens)-2)
	for _, token := range tokens[2:] {
		if token = strings.TrimSpace(token); token != "" {
			words = append(words, token)
		}
	}
	var values kustomizeBuildFlagValues
	flags := newKustomizeBuildFlagSet(&values)
	options, operands := splitKustomizeBuildOperands(flags, words)
	for _, operand := range operands {
		if operand != "." {
			return nil, fmt.Errorf("unsupported kustomize build path or remote operand")
		}
	}
	if _, err := kustomizeBuildSettingsFor(RenderOptions{KustomizeCaller: KustomizeCallerCommand, BuildOptions: options}); err != nil {
		return nil, fmt.Errorf("unsupported kustomize build option: %w", err)
	}
	return options, nil
}

// splitKustomizeBuildOperands separates the operands of words from the
// flags and their values, as pflag reads them (FlagSet.parseArgs in
// github.com/spf13/pflag v1.0.10): "--" ends the flags, a word that does
// not start with "-", or is "-", is an operand, and a flag that takes a
// value and has no "=" takes the next word.
func splitKustomizeBuildOperands(flags *pflag.FlagSet, words []string) (options, operands []string) {
	for i := 0; i < len(words); i++ {
		word := words[i]
		if word == "--" {
			options = append(options, word)
			operands = append(operands, words[i+1:]...)
			return options, operands
		}
		if len(word) < 2 || word[0] != '-' {
			operands = append(operands, word)
			continue
		}
		options = append(options, word)
		if takesKustomizeBuildFlagValue(flags, word) && i+1 < len(words) {
			i++
			options = append(options, words[i])
		}
	}
	return options, operands
}

// takesKustomizeBuildFlagValue reports whether the flag word takes the
// next word as its value.
func takesKustomizeBuildFlagValue(flags *pflag.FlagSet, word string) bool {
	if strings.HasPrefix(word, "--") {
		name := word[2:]
		if strings.Contains(name, "=") {
			return false
		}
		flag := flags.Lookup(name)
		return flag != nil && flag.NoOptDefVal == ""
	}
	shorthands := word[1:]
	for len(shorthands) > 0 {
		flag := flags.ShorthandLookup(shorthands[:1])
		rest := shorthands[1:]
		if flag == nil || strings.HasPrefix(rest, "=") {
			return false
		}
		if flag.NoOptDefVal != "" {
			shorthands = rest
			continue
		}
		return rest == ""
	}
	return false
}
