package config

import (
	"encoding/csv"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ArgoCDComponent names an Argo CD program that reads an
// argocd-cmd-params-cm key that drydock uses. The key reaches the program
// only as an environment variable of its container, which the install maps
// from the ConfigMap with a configMapKeyRef. A command-line flag of the
// program, where one exists, wins over the environment variable: the
// program reads the variable only as the default value of the flag.
type ArgoCDComponent string

const (
	// RepoServerComponent is argocd-repo-server (Argo CD v3.6.1
	// cmd/argocd-repo-server/commands/argocd_repo_server.go).
	RepoServerComponent ArgoCDComponent = "argocd-repo-server"
	// ApplicationControllerComponent is argocd-application-controller
	// (cmd/argocd-application-controller/commands/argocd_application_controller.go).
	ApplicationControllerComponent ArgoCDComponent = "argocd-application-controller"
	// ApplicationSetControllerComponent is argocd-applicationset-controller
	// (cmd/argocd-applicationset-controller/commands/applicationset_controller.go).
	ApplicationSetControllerComponent ArgoCDComponent = "argocd-applicationset-controller"
)

// ArgoCDComponents lists the components in a fixed order.
var ArgoCDComponents = []ArgoCDComponent{RepoServerComponent, ApplicationControllerComponent, ApplicationSetControllerComponent}

type componentFlagKind int

const (
	componentFlagNone componentFlagKind = iota
	componentFlagBool
	componentFlagString
	componentFlagStringSlice
	componentFlagDuration
)

// ComponentSetting is one argocd-cmd-params-cm key that a component reads
// from the environment variable Env and, when Flag is set, from that flag.
type ComponentSetting struct {
	Key  string
	Env  string
	Flag string
	kind componentFlagKind
}

// componentSettings maps each component to the keys that drydock applies
// (applyCommandParameterValue), with the variable and the flag of Argo CD
// v3.6.1:
//   - argocd_repo_server.go:50 (ARGOCD_GIT_MODULES_ENABLED, no flag), :264,
//     :266, and :274;
//   - argocd_application_controller.go:286, :299, and :300;
//   - applicationset_controller.go:55 (ARGOCD_GIT_MODULES_ENABLED, no flag),
//     :316, :327, and :329.
var componentSettings = map[ArgoCDComponent][]ComponentSetting{
	RepoServerComponent: {
		{Key: maxCombinedDirectoryManifestsSizeKey, Env: "ARGOCD_REPO_SERVER_MAX_COMBINED_DIRECTORY_MANIFESTS_SIZE", Flag: "max-combined-directory-manifests-size", kind: componentFlagString},
		{Key: allowOutOfBoundsSymlinksKey, Env: "ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS", Flag: "allow-oob-symlinks", kind: componentFlagBool},
		{Key: includeHiddenDirectoriesKey, Env: "ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES", Flag: "include-hidden-directories", kind: componentFlagBool},
		{Key: enableGitSubmoduleKey, Env: "ARGOCD_GIT_MODULES_ENABLED"},
	},
	ApplicationControllerComponent: {
		{Key: applicationNamespacesKey, Env: "ARGOCD_APPLICATION_NAMESPACES", Flag: "application-namespaces", kind: componentFlagStringSlice},
		{Key: controllerServerSideDiffKey, Env: "ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF", Flag: "server-side-diff-enabled", kind: componentFlagBool},
		{Key: ignoreNormalizerJQTimeoutKey, Env: "ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT", Flag: "ignore-normalizer-jq-execution-timeout-seconds", kind: componentFlagDuration},
	},
	ApplicationSetControllerComponent: {
		{Key: applicationSetNamespacesKey, Env: "ARGOCD_APPLICATIONSET_CONTROLLER_NAMESPACES", Flag: "applicationset-namespaces", kind: componentFlagStringSlice},
		{Key: enableProgressiveSyncsKey, Env: "ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS", Flag: "enable-progressive-syncs", kind: componentFlagBool},
		{Key: enableNewGitFileGlobbingKey, Env: "ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_NEW_GIT_FILE_GLOBBING", Flag: "enable-new-git-file-globbing", kind: componentFlagBool},
		{Key: appSetEnableGitSubmoduleKey, Env: "ARGOCD_GIT_MODULES_ENABLED"},
	},
}

// componentBoolFlags lists every other boolean flag of each program. A
// boolean flag takes no separate value, so "--flag value" leaves value as
// an argument. Every other flag takes the next argument as its value. The
// lists come from the BoolVar calls of Argo CD v3.6.1 in the command files
// above, util/cache/cache.go:253 and :259 (redis-use-tls and
// redis-insecure-skip-tls-verify, which the repo-server and the application
// controller add), and the kubectl flags of the two controllers
// (insecure-skip-tls-verify and disable-compression, client-go
// tools/clientcmd/overrides.go:241 and :244 at v0.37.0). cobra adds help.
var componentBoolFlags = map[ArgoCDComponent][]string{
	RepoServerComponent: {
		"otlp-insecure", "disable-oci-manifest-max-extracted-size", "disable-helm-manifest-max-extracted-size",
		"plugin-use-manifest-generate-paths", "enable-builtin-git-config", "disable-tls",
		"redis-use-tls", "redis-insecure-skip-tls-verify", "help",
	},
	ApplicationControllerComponent: {
		"repo-server-plaintext", "repo-server-strict-tls", "otlp-insecure", "persist-resource-health",
		"dynamic-cluster-distribution-enabled", "hydrator-enabled",
		"redis-use-tls", "redis-insecure-skip-tls-verify", "insecure-skip-tls-verify", "disable-compression", "help",
	},
	ApplicationSetControllerComponent: {
		"enable-leader-election", "enable-policy-override", "debug", "enable-scm-providers", "dry-run",
		"token-ref-strict-mode", "repo-server-plaintext", "repo-server-strict-tls", "enable-github-api-metrics",
		"insecure-skip-tls-verify", "disable-compression", "help",
	},
}

// componentValueFlags lists every flag of each program that takes a value,
// other than the setting flags. With componentBoolFlags and the setting
// flags, they are all the flags of the program, so any other flag stops it
// at startup ("unknown flag"). They come from the flag calls of Argo CD
// v3.6.1 in the command files above, with the util/cli helpers such as
// BoundedFloat64Var (otlp-sample-ratio), util/cache/cache.go
// (AddCacheFlagsToCmd), reposerver/cache/cache.go:74-76,
// util/cache/appstate/cache.go:37, util/tls/tls.go (AddTLSFlagsToCmd for
// the repo-server, AddClientTLSFlagsToCmdWithPrefix for the controllers),
// and util/cli/cli.go:105-119 with the client-go override flags
// (tools/clientcmd/overrides.go:147-166 at v0.37.0) for the controllers.
var componentValueFlags = map[ArgoCDComponent][]string{
	RepoServerComponent: append([]string{
		"logformat", "loglevel", "parallelismlimit", "address", "port", "metrics-address", "metrics-port",
		"otlp-address", "otlp-sample-ratio", "otlp-headers", "otlp-attrs", "plugin-tar-exclude", "streamed-manifest-max-tar-size",
		"streamed-manifest-max-extracted-size", "helm-manifest-max-extracted-size", "helm-registry-max-index-size",
		"oci-manifest-max-extracted-size", "oci-layer-media-types", "client-ca-path",
		"tlsminversion", "tlsmaxversion", "tlsciphers", "tlscurvepreferences",
		"repo-cache-expiration", "revision-cache-expiration", "revision-cache-lock-timeout",
	}, cacheValueFlags...),
	ApplicationControllerComponent: append(append([]string{
		"app-resync", "app-hard-resync", "app-resync-jitter", "repo-error-grace-period-seconds", "repo-server",
		"repo-server-timeout-seconds", "commit-server", "status-processors", "operation-processors",
		"hydration-processors", "logformat", "loglevel", "gloglevel", "metrics-port", "metrics-cache-expiration",
		"self-heal-timeout-seconds", "self-heal-backoff-timeout-seconds", "self-heal-backoff-factor",
		"self-heal-backoff-cap-seconds", "self-heal-backoff-cooldown-seconds", "sync-timeout",
		"kubectl-parallelism-limit", "metrics-application-labels", "metrics-application-conditions",
		"metrics-cluster-labels", "otlp-address", "otlp-sample-ratio", "otlp-headers", "otlp-attrs", "sharding-method",
		"wq-bucket-size", "wq-bucket-qps", "wq-cooldown-ns", "wq-basedelay-ns", "wq-maxdelay-ns",
		"wq-backoff-factor", "enable-k8s-event", "app-state-cache-expiration",
	}, cacheValueFlags...), controllerClientValueFlags...),
	ApplicationSetControllerComponent: append([]string{
		"metrics-addr", "probe-addr", "webhook-addr", "argocd-repo-server", "policy", "logformat", "loglevel",
		"allowed-scm-providers", "refresh-grace-period-seconds", "repo-server-timeout-seconds",
		"concurrent-reconciliations", "scm-root-ca-path", "scm-proxy-url", "scm-no-proxy",
		"preserved-annotations", "preserved-labels", "webhook-parallelism-limit",
		"metrics-applicationset-labels", "max-resources-status-count", "cache-sync-period",
		"concurrent-application-updates", "wq-bucket-size", "wq-bucket-qps", "wq-cooldown", "wq-basedelay",
		"wq-maxdelay", "wq-backoff-factor",
	}, controllerClientValueFlags...),
}

// cacheValueFlags are the value flags of util/cache.AddCacheFlagsToCmd.
var cacheValueFlags = []string{
	"redis", "redisdb", "sentinel", "sentinelmaster", "default-cache-expiration", "redis-client-certificate",
	"redis-client-key", "redis-ca-certificate", "redis-compress",
}

// controllerClientValueFlags are the value flags of the repo-server client
// TLS flags and the kubectl flags of the two controllers.
var controllerClientValueFlags = []string{
	"repo-server-ca-cert-path", "repo-server-client-cert-path", "repo-server-client-cert-key-path",
	"kubeconfig", "context", "request-timeout", "client-certificate", "client-key", "token", "as", "as-uid",
	"as-group", "username", "password", "server", "certificate-authority", "tls-server-name", "proxy-url",
	"cluster", "user", "namespace",
}

// componentValueShorthands lists the one-letter flags that take a value:
// the kubectl -n of the two controllers. cobra adds -h, which prints the
// help.
var componentValueShorthands = map[ArgoCDComponent]string{
	ApplicationControllerComponent:    "n",
	ApplicationSetControllerComponent: "n",
}

// ComponentSettings returns the settings that component reads.
func ComponentSettings(component ArgoCDComponent) []ComponentSetting {
	return componentSettings[component]
}

// ComponentSettingEnv reports whether name is the environment variable of a
// setting of component.
func ComponentSettingEnv(component ArgoCDComponent, name string) bool {
	for _, setting := range componentSettings[component] {
		if setting.Env == name {
			return true
		}
	}
	return false
}

// ComponentRunsProgram reports whether the command line of a container,
// command and then args, runs the program of component: its first item
// names the program, such as /usr/local/bin/argocd-repo-server, or tini
// runs it (componentProgramIndex). A container without a command line,
// such as one in a Kustomize patch, or one that runs another program, such
// as a CMP sidecar or a shell, does not.
func ComponentRunsProgram(component ArgoCDComponent, command, args []string) bool {
	return componentProgramIndex(component, componentCommandLine(command, args)) >= 0
}

func componentCommandLine(command, args []string) []string {
	return append(append([]string(nil), command...), args...)
}

// componentProgramIndex returns the index of the program of component in
// line, or -1. The Argo CD image starts the program through tini (its
// ENTRYPOINT is /usr/bin/tini --), so a line that names tini first runs
// the program after the options of tini, up to "--". The -p and -e options
// of tini take a value.
func componentProgramIndex(component ArgoCDComponent, line []string) int {
	index := 0
	if len(line) > 0 && path.Base(line[0]) == "tini" {
		index = 1
		for index < len(line) && strings.HasPrefix(line[index], "-") {
			option := line[index]
			index++
			if option == "--" {
				break
			}
			if option == "-p" || option == "-e" {
				index++
			}
		}
	}
	if index < len(line) && path.Base(line[index]) == string(component) {
		return index
	}
	return -1
}

// ComponentFlag is the value of a setting flag on the command line of a
// component: the value text that drydock applies as the setting, as the
// program reads it.
type ComponentFlag struct {
	Flag  string
	Value string
}

// ParseComponentArgs reads the setting flags of component from the command
// and args of its container, as the kubelet builds the command line
// (command, then args, or the image entrypoint with args) and as the Argo CD
// program parses it with pflag v1.0.10 (FlagSet.parseArgs). It returns the
// flags that set a setting and notes about what drydock cannot know: a
// command that does not run the program, an argument with a $(VAR)
// reference, or a flag value that stops the program at startup. A flag that
// stops the program is left out.
//
// A boolean flag is true without a value, or takes the value after "=",
// which strconv.ParseBool reads. A string or duration flag takes the value
// after "=" or the next argument, and the last one wins. A string slice
// flag reads each value as one CSV line and adds its fields to those of
// the earlier ones. "--" ends the flags.
func ParseComponentArgs(component ArgoCDComponent, command, args []string) ([]ComponentFlag, []string) {
	line := componentCommandLine(command, args)
	if len(line) == 0 {
		return nil, nil
	}
	program := componentProgramIndex(component, line)
	if program < 0 {
		if hasSettingFlagText(component, line) {
			return nil, []string{fmt.Sprintf("the container runs %q, not %s, so drydock does not read its flags", line[0], component)}
		}
		return nil, nil
	}
	parser := componentArgsParser{component: component, values: map[string]*componentFlagValue{}}
	parser.parse(line[program+1:])
	var flags []ComponentFlag
	for _, setting := range componentSettings[component] {
		if value, ok := parser.values[setting.Flag]; ok && setting.Flag != "" {
			flags = append(flags, ComponentFlag{Flag: setting.Flag, Value: value.text()})
		}
	}
	return flags, parser.notes
}

func hasSettingFlagText(component ArgoCDComponent, line []string) bool {
	for _, arg := range line {
		for _, setting := range componentSettings[component] {
			if setting.Flag != "" && strings.Contains(arg, "--"+setting.Flag) {
				return true
			}
		}
	}
	return false
}

type componentFlagValue struct {
	kind  componentFlagKind
	value string
	list  []string
}

// text returns the value as the setting text that drydock applies with
// the environment variable rules: "true" or "false" for a boolean, the
// fields joined with commas for a string slice.
func (v *componentFlagValue) text() string {
	if v.kind == componentFlagStringSlice {
		return strings.Join(v.list, ",")
	}
	return v.value
}

type componentArgsParser struct {
	component ArgoCDComponent
	values    map[string]*componentFlagValue
	notes     []string
}

func (p *componentArgsParser) parse(args []string) {
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		switch {
		case arg == "--":
			return
		case strings.Contains(arg, "$(") && !strings.HasPrefix(arg, "--"):
			// The kubelet fills in the reference before the program
			// parses its flags, so it can become any flag.
			p.notes = append(p.notes, fmt.Sprintf("the argument %q has a $(VAR) reference, which the kubelet fills in from the container environment, so it can set a flag", arg))
		case len(arg) < 2 || arg[0] != '-':
			continue
		case arg[1] == '-':
			args = p.parseLong(arg, args)
		default:
			args = p.parseShort(arg, args)
		}
	}
}

// parseShort skips a group of one-letter flags. -n of the controllers
// takes a value: the rest of the group, the text after "=", or the next
// argument. -h prints the help, and any other letter stops the program.
func (p *componentArgsParser) parseShort(arg string, args []string) []string {
	shorthands := arg[1:]
	valueShorthand := componentValueShorthands[p.component]
	for i := range len(shorthands) {
		switch {
		case shorthands[i] == 'h':
			p.notes = append(p.notes, fmt.Sprintf("flag -h prints the help of %s, so %s does not start", p.component, p.component))
			return args
		case valueShorthand == "" || shorthands[i] != valueShorthand[0]:
			p.notes = append(p.notes, fmt.Sprintf("flag -%c is not a flag of %s, so %s does not start", shorthands[i], p.component, p.component))
			return args
		case i+1 < len(shorthands) || len(args) == 0:
			return args
		default:
			return args[1:]
		}
	}
	return args
}

func (p *componentArgsParser) parseLong(arg string, args []string) []string {
	name, value, inline := strings.Cut(arg[2:], "=")
	setting, isSetting := p.setting(name)
	if !isSetting {
		return p.skipLong(name, inline, args)
	}
	if setting.kind == componentFlagBool {
		if !inline {
			value = "true"
		}
		p.setBool(setting, value)
		return args
	}
	if !inline {
		if len(args) == 0 {
			p.notes = append(p.notes, fmt.Sprintf("flag --%s has no value, so %s does not start", setting.Flag, p.component))
			return args
		}
		value = args[0]
		args = args[1:]
	}
	p.setValue(setting, value)
	return args
}

func (p *componentArgsParser) setting(name string) (ComponentSetting, bool) {
	for _, setting := range componentSettings[p.component] {
		if setting.Flag != "" && setting.Flag == name {
			return setting, true
		}
	}
	return ComponentSetting{}, false
}

// skipLong skips a flag that sets no setting. A boolean flag takes no
// separate value. A value flag takes the next argument, also one that
// starts with "--". help prints the help, and an unknown flag stops the
// program.
func (p *componentArgsParser) skipLong(name string, inline bool, args []string) []string {
	switch {
	case name == "help":
		p.notes = append(p.notes, fmt.Sprintf("flag --help prints the help of %s, so %s does not start", p.component, p.component))
		return args
	case strings.Contains(name, "$("):
		p.notes = append(p.notes, fmt.Sprintf("the argument %q has a $(VAR) reference, which the kubelet fills in from the container environment, so it can set a flag", "--"+name))
		return args
	case slices.Contains(componentBoolFlags[p.component], name):
		return args
	case !slices.Contains(componentValueFlags[p.component], name):
		p.notes = append(p.notes, fmt.Sprintf("flag --%s is not a flag of %s, so %s does not start", name, p.component, p.component))
		return args
	case inline || len(args) == 0:
		return args
	default:
		return args[1:]
	}
}

func (p *componentArgsParser) setBool(setting ComponentSetting, value string) {
	if strings.Contains(value, "$(") {
		p.notes = append(p.notes, fmt.Sprintf("flag --%s has a $(VAR) reference, which the kubelet fills in from the container environment", setting.Flag))
		return
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		p.notes = append(p.notes, fmt.Sprintf("flag --%s has the value %q, which is not a boolean, so %s does not start", setting.Flag, value, p.component))
		return
	}
	p.values[setting.Flag] = &componentFlagValue{kind: componentFlagBool, value: strconv.FormatBool(parsed)}
}

func (p *componentArgsParser) setValue(setting ComponentSetting, value string) {
	if strings.Contains(value, "$(") {
		p.notes = append(p.notes, fmt.Sprintf("flag --%s has a $(VAR) reference, which the kubelet fills in from the container environment", setting.Flag))
		return
	}
	switch setting.kind {
	case componentFlagDuration:
		if _, err := time.ParseDuration(value); err != nil {
			p.notes = append(p.notes, fmt.Sprintf("flag --%s has the value %q, which is not a duration, so %s does not start", setting.Flag, value, p.component))
			return
		}
	case componentFlagStringSlice:
		fields, err := readComponentFlagCSV(value)
		if err != nil {
			p.notes = append(p.notes, fmt.Sprintf("flag --%s has the value %q, which is not a CSV line, so %s does not start", setting.Flag, value, p.component))
			return
		}
		for _, field := range fields {
			if strings.Contains(field, ",") || strings.TrimSpace(field) != field {
				p.notes = append(p.notes, fmt.Sprintf("flag --%s has the entry %q, which drydock reads with the spaces around it removed and split at each comma", setting.Flag, field))
			}
		}
		current, ok := p.values[setting.Flag]
		if !ok {
			current = &componentFlagValue{kind: componentFlagStringSlice}
			p.values[setting.Flag] = current
		}
		current.list = append(current.list, fields...)
		return
	case componentFlagString:
		if value == "" {
			p.notes = append(p.notes, fmt.Sprintf("flag --%s has an empty value, which is not a Kubernetes quantity, so %s does not start", setting.Flag, p.component))
			return
		}
	case componentFlagNone, componentFlagBool:
		// setBool reads boolean flags, and a setting without a flag has
		// none to read.
	}
	p.values[setting.Flag] = &componentFlagValue{kind: setting.kind, value: value}
}

// readComponentFlagCSV reads a string slice flag value as pflag does
// (readAsCSV in string_slice.go): an empty value has no fields.
func readComponentFlagCSV(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	return csv.NewReader(strings.NewReader(value)).Read()
}

// ApplyComponentSetting stores value as the setting of key.
func ApplyComponentSetting(settings *ArgoSettings, key string, value Value[string]) {
	applyCommandParameterValue(settings, key, value)
}

// ComponentSettingValue returns the setting that settings holds for key.
func ComponentSettingValue(settings ArgoSettings, key string) Value[string] {
	switch key {
	case maxCombinedDirectoryManifestsSizeKey:
		return settings.MaxCombinedDirectoryManifestsSize
	case allowOutOfBoundsSymlinksKey:
		return settings.AllowOutOfBoundsSymlinks
	case enableProgressiveSyncsKey:
		return settings.EnableProgressiveSyncs
	case enableNewGitFileGlobbingKey:
		return settings.EnableNewGitFileGlobbing
	case includeHiddenDirectoriesKey:
		return settings.IncludeHiddenDirectories
	case enableGitSubmoduleKey:
		return settings.EnableGitSubmodules
	case appSetEnableGitSubmoduleKey:
		return settings.AppSetEnableGitSubmodules
	case ignoreNormalizerJQTimeoutKey:
		return settings.IgnoreNormalizerJQTimeout
	case controllerServerSideDiffKey:
		return settings.ControllerServerSideDiff
	case applicationNamespacesKey:
		return settings.ApplicationNamespaces
	case applicationSetNamespacesKey:
		return settings.ApplicationSetNamespaces
	default:
		return Value[string]{}
	}
}
