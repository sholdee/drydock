package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
	"github.com/sholdee/drydock/internal/requestopts"
	"github.com/sholdee/drydock/internal/source"
	"github.com/spf13/cobra"
)

type commonFlags struct {
	path                     string
	pathOrig                 string
	repo                     string
	ref                      string
	refOrig                  string
	selector                 string
	discoveryMode            string
	maxDiscoveryDepth        int
	maxDiscoveryDepthSet     bool
	discoverKustomize        []string
	discoverIgnore           []string
	repoMaps                 []string
	offline                  bool
	refreshCharts            bool
	chartCacheDir            string
	gitCacheDir              string
	refreshGit               bool
	gitUsername              string
	gitPassword              string
	gitBearerToken           string
	gitSSHKeyFile            string
	gitSSHPassphrase         string
	gitKnownHostsFile        string
	helmUsername             string
	helmPassword             string
	helmBearerToken          string
	registryConfig           string
	refreshRemotes           bool
	remoteCacheDir           string
	ociCacheDir              string
	ociUsername              string
	ociPassword              string
	ociCAFile                string
	ociClientCertFile        string
	ociClientKeyFile         string
	ociInsecureSkipVerify    bool
	remoteUsername           string
	remotePassword           string
	remoteBearerToken        string
	enableAVPCompat          bool
	enableKSOPSCompat        bool
	enablePlugins            bool
	pluginCacheDir           string
	pluginPolicyPath         string
	pluginPolicyPathExplicit bool
	pluginPolicyRef          string
	pluginPolicyRepo         string
	disablePluginPolicy      bool
	appsetFixtures           []string
	skipKinds                []string
	skipCRDs                 bool
	skipSecrets              bool
	skipLuaHealth            bool
	changedOnly              bool
	changedOnlyIncludes      []string
	changedOnlyIgnores       []string
	strictChangedOnly        bool
	strict                   bool
	projectDiagnostics       string
	exitCode                 bool
	output                   string
	unified                  int
	stripAttrs               []string
	showIgnoredFields        bool
	limitBytes               int
	cacheEvents              bool
	parallelism              int
	renderCache              bool
	renderCacheDir           string
	renderCacheMaxSize       quantityFlag
	refreshRenders           bool
	engineFingerprint        rendercache.EngineFingerprint
	kubeVersion              string
	apiVersions              []string
	noCRDScope               bool
}

func defaultCommonFlags() commonFlags {
	return commonFlags{
		path:               ".",
		changedOnly:        true,
		exitCode:           true,
		output:             "diff",
		unified:            3,
		limitBytes:         65536,
		parallelism:        1,
		renderCache:        true,
		renderCacheMaxSize: defaultRenderCacheMaxSize(),
		discoveryMode:      "fleet",
		maxDiscoveryDepth:  4,
		projectDiagnostics: string(diagnostic.ProjectDiagnosticsModeActionable),
	}
}

func bindCommonFlags(cmd *cobra.Command, flags *commonFlags) {
	bindPathDiscoveryFlags(cmd, flags)
	bindAcquisitionCacheFlags(cmd, flags)
	bindAuthFlags(cmd, flags)
	bindRemoteAcquisitionCacheFlags(cmd, flags)
	bindPluginFlags(cmd, flags)
	bindApplicationSetFixtureFlags(cmd, flags)
	bindFilterFlags(cmd, flags)
	bindStrictFlag(cmd, flags)
	bindProjectDiagnosticsFlag(cmd, flags)
	bindOutputFlags(cmd, flags)
	bindDiagnosticsCacheEventFlags(cmd, flags)
	bindParallelismFlags(cmd, flags)
	bindRenderCacheFlags(cmd, flags)
}

func bindPathDiscoveryFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.path, "path", flags.path, "repository path to inspect")
	cmd.Flags().StringVarP(&flags.selector, "selector", "l", flags.selector, "label selector for Applications")
	cmd.Flags().StringVar(&flags.discoveryMode, "discovery-mode", flags.discoveryMode, "Application discovery mode: fleet or static")
	cmd.Flags().IntVar(&flags.maxDiscoveryDepth, "max-discovery-depth", flags.maxDiscoveryDepth, "maximum recursive rendered Application discovery depth")
	cmd.Flags().StringArrayVar(&flags.discoverKustomize, "discover-kustomize", flags.discoverKustomize, "additional local Kustomize path to render for Argo CD Application discovery")
	cmd.Flags().StringArrayVar(&flags.discoverIgnore, "discover-ignore", flags.discoverIgnore, "repository-relative glob excluded from discovery before decoding")
	cmd.Flags().StringArrayVar(&flags.repoMaps, "repo-map", flags.repoMaps, "repository URL mapping in from=to form")
}

func bindAcquisitionCacheFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.offline, "offline", flags.offline, "disable network access and use local files, repo maps, or existing caches")
	cmd.Flags().BoolVar(&flags.refreshCharts, "refresh-charts", flags.refreshCharts, "refresh cached Helm charts before rendering")
	cmd.Flags().StringVar(&flags.chartCacheDir, "chart-cache-dir", flags.chartCacheDir, "directory for cached Helm charts")
	cmd.Flags().StringVar(&flags.gitCacheDir, "git-cache-dir", flags.gitCacheDir, "directory for cached Git repositories")
	cmd.Flags().BoolVar(&flags.refreshGit, "refresh-git", flags.refreshGit, "fetch cached Git repositories before rendering")
	cmd.Flags().StringVar(&flags.ociCacheDir, "oci-cache-dir", flags.ociCacheDir, "directory for cached OCI artifact sources")
}

func bindAuthFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.gitUsername, "git-username", flags.gitUsername, "username for authenticated Git HTTPS sources")
	cmd.Flags().StringVar(&flags.gitPassword, "git-password", flags.gitPassword, "password for authenticated Git HTTPS sources")
	cmd.Flags().StringVar(&flags.gitBearerToken, "git-bearer-token", flags.gitBearerToken, "bearer token for authenticated Git HTTPS sources")
	cmd.Flags().StringVar(&flags.gitSSHKeyFile, "git-ssh-key-file", flags.gitSSHKeyFile, "private key file for authenticated Git SSH sources")
	cmd.Flags().StringVar(&flags.gitSSHPassphrase, "git-ssh-passphrase", flags.gitSSHPassphrase, "passphrase for encrypted Git SSH private keys")
	cmd.Flags().StringVar(&flags.gitKnownHostsFile, "git-known-hosts-file", flags.gitKnownHostsFile, "known_hosts file for authenticated Git SSH sources")
	cmd.Flags().StringVar(&flags.helmUsername, "helm-username", flags.helmUsername, "username for authenticated HTTP Helm repositories")
	cmd.Flags().StringVar(&flags.helmPassword, "helm-password", flags.helmPassword, "password for authenticated HTTP Helm repositories")
	cmd.Flags().StringVar(&flags.helmBearerToken, "helm-bearer-token", flags.helmBearerToken, "bearer token for authenticated HTTP Helm repositories")
	cmd.Flags().StringVar(&flags.registryConfig, "registry-config", flags.registryConfig, "Helm OCI registry config file")
	cmd.Flags().StringVar(&flags.ociUsername, "oci-username", flags.ociUsername, "username for authenticated OCI artifact registries")
	cmd.Flags().StringVar(&flags.ociPassword, "oci-password", flags.ociPassword, "password for authenticated OCI artifact registries")
	cmd.Flags().StringVar(&flags.ociCAFile, "oci-ca-file", flags.ociCAFile, "PEM CA bundle for OCI artifact registry TLS (replaces the system pool for all OCI registries)")
	cmd.Flags().StringVar(&flags.ociClientCertFile, "oci-client-cert-file", flags.ociClientCertFile, "client certificate file for mutual-TLS OCI artifact registries (requires --oci-client-key-file)")
	cmd.Flags().StringVar(&flags.ociClientKeyFile, "oci-client-key-file", flags.ociClientKeyFile, "client key file for mutual-TLS OCI artifact registries (requires --oci-client-cert-file)")
	cmd.Flags().BoolVar(&flags.ociInsecureSkipVerify, "oci-insecure-skip-verify", flags.ociInsecureSkipVerify, "skip TLS certificate verification for OCI artifact registries (insecure; credentials are exposed to whoever answers)")
	cmd.Flags().StringVar(&flags.remoteUsername, "remote-username", flags.remoteUsername, "username for authenticated remote Kustomize HTTP resources")
	cmd.Flags().StringVar(&flags.remotePassword, "remote-password", flags.remotePassword, "password for authenticated remote Kustomize HTTP resources")
	cmd.Flags().StringVar(&flags.remoteBearerToken, "remote-bearer-token", flags.remoteBearerToken, "bearer token for authenticated remote Kustomize HTTP resources")
}

func bindRemoteAcquisitionCacheFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.refreshRemotes, "refresh-remotes", flags.refreshRemotes, "refresh cached remote Kustomize resources before rendering")
	cmd.Flags().StringVar(&flags.remoteCacheDir, "remote-cache-dir", flags.remoteCacheDir, "directory for cached remote Kustomize resources")
}

func bindPluginFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.enableAVPCompat, "enable-avp-compat", flags.enableAVPCompat, "force argocd-vault-plugin placeholder redaction for native-rendered sources")
	cmd.Flags().BoolVar(&flags.enableKSOPSCompat, "enable-ksops-compat", flags.enableKSOPSCompat, "render KSOPS kustomize generators as deterministic placeholder manifests without decryption")
	cmd.Flags().BoolVar(&flags.enablePlugins, "enable-plugins", flags.enablePlugins, "enable trusted exec/container plugin policy entries")
	cmd.Flags().StringVar(&flags.pluginCacheDir, "plugin-cache-dir", flags.pluginCacheDir, "directory for policy-managed container plugin caches")
	cmd.Flags().StringVar(&flags.pluginPolicyPath, "plugin-policy-path", flags.pluginPolicyPath, "trusted plugin policy path relative to the selected policy root")
	cmd.Flags().StringVar(&flags.pluginPolicyRef, "plugin-policy-ref", flags.pluginPolicyRef, "Git ref to use as the trusted plugin policy source")
	cmd.Flags().StringVar(&flags.pluginPolicyRepo, "plugin-policy-repo", flags.pluginPolicyRepo, "local Git repository path used to resolve --plugin-policy-ref")
	cmd.Flags().BoolVar(&flags.disablePluginPolicy, "disable-plugin-policy", flags.disablePluginPolicy, "disable trusted plugin policy loading")
}

func bindApplicationSetFixtureFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringArrayVar(&flags.appsetFixtures, "appset-provider-fixture", flags.appsetFixtures, "local YAML/JSON fixture file for provider-backed ApplicationSet generators")
}

func bindFilterFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringArrayVar(&flags.skipKinds, "skip-kind", flags.skipKinds, "rendered resource kind to omit from output and diffs")
	cmd.Flags().BoolVar(&flags.skipCRDs, "skip-crds", flags.skipCRDs, "omit CustomResourceDefinition resources from output and diffs")
	cmd.Flags().BoolVar(&flags.skipSecrets, "skip-secrets", flags.skipSecrets, "omit Secret resources from output and diffs")
	cmd.Flags().StringVar(&flags.kubeVersion, "kube-version", flags.kubeVersion, "Kubernetes version for rendering capabilities (overrides per-app kubeVersion)")
	cmd.Flags().StringArrayVar(&flags.apiVersions, "api-versions", flags.apiVersions, "additional Kubernetes API versions for capability-gated rendering, unioned with per-app apiVersions (e.g. monitoring.coreos.com/v1)")
	cmd.Flags().BoolVar(&flags.noCRDScope, "no-crd-scope", flags.noCRDScope, "disable post-render cluster-scoped custom resource namespace normalization")
	_ = cmd.Flags().MarkHidden("no-crd-scope")
}

func bindDiffPathFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.pathOrig, "path-orig", flags.pathOrig, "baseline repository path for diffs")
}

func bindChangedOnlyFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.changedOnly, "changed-only", flags.changedOnly, "limit work to Applications affected by changed files")
	cmd.Flags().BoolVar(&flags.strictChangedOnly, "strict-changed-only", flags.strictChangedOnly, "fail when changed-only input ownership is ambiguous or incomplete")
}

func bindChangedOnlyPathFilterFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringArrayVar(&flags.changedOnlyIncludes, "changed-only-include", flags.changedOnlyIncludes, "repository-relative glob for changed paths considered by changed-only selection")
	cmd.Flags().StringArrayVar(&flags.changedOnlyIgnores, "changed-only-ignore", flags.changedOnlyIgnores, "repository-relative glob ignored by changed-only selection")
}

func bindStrictFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.strict, "strict", flags.strict, "promote diagnostics to errors")
}

func bindProjectDiagnosticsFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.projectDiagnostics, "project-diagnostics", flags.projectDiagnostics, "AppProject diagnostics mode: actionable, all, or off")
}

func bindDiffExitFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.exitCode, "exit-code", flags.exitCode, "return exit code 1 when a diff is found")
}

func bindOutputFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVarP(&flags.output, "output", "o", flags.output, "output format")
	cmd.Flags().IntVar(&flags.limitBytes, "limit-bytes", flags.limitBytes, "maximum bytes of rendered output per object")
}

func bindDiffFormattingFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().IntVarP(&flags.unified, "unified", "u", flags.unified, "number of unified diff context lines")
	cmd.Flags().StringArrayVar(&flags.stripAttrs, "strip-attr", flags.stripAttrs, "metadata label or annotation key to strip before diffing")
}

func bindDiagnosticsCacheEventFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.cacheEvents, "cache-events", flags.cacheEvents, "report cache events (stderr for build/test, structured output for diag)")
}

// renderCacheEventsText writes one stderr line per cache event. Events are
// observability output: they never belong on stdout, which stays
// machine-parseable for manifests and statuses.
func renderCacheEventsText(w io.Writer, events []cacheevent.Event) error {
	for _, event := range events {
		if _, err := fmt.Fprintf(w, "cache %s %s", event.Source, event.Action); err != nil {
			return err
		}
		if event.Target != "" {
			if _, err := fmt.Fprintf(w, " target=%s", event.Target); err != nil {
				return err
			}
		}
		if event.Revision != "" {
			if _, err := fmt.Fprintf(w, " revision=%s", event.Revision); err != nil {
				return err
			}
		}
		if event.Reason != "" {
			if _, err := fmt.Fprintf(w, " reason=%s", event.Reason); err != nil {
				return err
			}
		}
		if event.Error != "" {
			if _, err := fmt.Fprintf(w, " error=%q", event.Error); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

func bindParallelismFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().IntVar(&flags.parallelism, "parallelism", flags.parallelism, "maximum number of Applications to render concurrently")
}

func bindLuaHealthTestFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.skipLuaHealth, "skip-lua-health", flags.skipLuaHealth, "skip Lua health validation while testing Applications")
}

func bindShowIgnoredFieldsFlag(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().BoolVar(&flags.showIgnoredFields, "show-ignored-fields", flags.showIgnoredFields, "show drydock default ignored diff fields")
}

func bindDiffRefFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVar(&flags.repo, "repo", flags.repo, "local Git repository path used to resolve --ref and --ref-orig")
	cmd.Flags().StringVar(&flags.ref, "ref", flags.ref, "Git ref to use for the current diff side")
	cmd.Flags().StringVar(&flags.refOrig, "ref-orig", flags.refOrig, "Git ref to use for the baseline diff side")
}

func (flags commonFlags) gitCredentials() source.GitCredentials {
	return source.GitCredentials{
		Username:          flags.gitUsername,
		Password:          flags.gitPassword,
		BearerToken:       flags.gitBearerToken,
		SSHPrivateKeyPath: flags.gitSSHKeyFile,
		SSHPassphrase:     flags.gitSSHPassphrase,
		SSHKnownHostsPath: flags.gitKnownHostsFile,
	}
}

func (flags commonFlags) remoteCredentials() remote.Credentials {
	return remote.Credentials{
		Username:    flags.remoteUsername,
		Password:    flags.remotePassword,
		BearerToken: flags.remoteBearerToken,
	}
}

func (flags commonFlags) remoteGitCredentials() remote.GitCredentials {
	return remote.GitCredentials{
		Username:          flags.gitUsername,
		Password:          flags.gitPassword,
		BearerToken:       flags.gitBearerToken,
		SSHPrivateKeyPath: flags.gitSSHKeyFile,
		SSHPassphrase:     flags.gitSSHPassphrase,
		SSHKnownHostsPath: flags.gitKnownHostsFile,
	}
}

func (flags commonFlags) chartCredentials() chart.ChartCredentials {
	return chart.ChartCredentials{
		Username:       flags.helmUsername,
		Password:       flags.helmPassword,
		BearerToken:    flags.helmBearerToken,
		RegistryConfig: flags.registryConfig,
	}
}

func (flags commonFlags) ociCredentials() ociartifact.Credentials {
	return ociartifact.Credentials{
		Username:           flags.ociUsername,
		Password:           flags.ociPassword,
		CAFile:             flags.ociCAFile,
		ClientCertFile:     flags.ociClientCertFile,
		ClientKeyFile:      flags.ociClientKeyFile,
		InsecureSkipVerify: flags.ociInsecureSkipVerify,
	}
}

func requestOptionsFromFlags(flags commonFlags, repoMaps []source.RepoMap) requestopts.Options {
	return requestopts.Options{
		Path:                           flags.path,
		LeftPath:                       flags.pathOrig,
		RightPath:                      flags.path,
		Repo:                           flags.repo,
		Ref:                            flags.ref,
		RefOrig:                        flags.refOrig,
		DiscoveryMode:                  flags.discoveryMode,
		MaxDiscoveryDepth:              flags.maxDiscoveryDepth,
		MaxDiscoveryDepthSet:           flags.maxDiscoveryDepthSet,
		DiscoverKustomizePaths:         append([]string(nil), flags.discoverKustomize...),
		DiscoverIgnores:                append([]string(nil), flags.discoverIgnore...),
		ChangedOnly:                    &flags.changedOnly,
		ChangedOnlyIncludes:            append([]string(nil), flags.changedOnlyIncludes...),
		ChangedOnlyIgnores:             append([]string(nil), flags.changedOnlyIgnores...),
		StrictChangedOnly:              flags.strictChangedOnly,
		Strict:                         flags.strict,
		ProjectDiagnosticsMode:         diagnostic.ProjectDiagnosticsMode(flags.projectDiagnostics),
		Unified:                        flags.unified,
		StripAttrs:                     append([]string(nil), flags.stripAttrs...),
		ShowIgnoredFields:              flags.showIgnoredFields,
		Offline:                        flags.offline,
		RefreshCharts:                  flags.refreshCharts,
		ChartCacheDir:                  flags.chartCacheDir,
		ChartCredentials:               flags.chartCredentials(),
		RepoMaps:                       repoMaps,
		GitCacheDir:                    flags.gitCacheDir,
		RefreshGit:                     flags.refreshGit,
		GitCredentials:                 flags.gitCredentials(),
		RefreshRemoteResources:         flags.refreshRemotes,
		RemoteResourceCacheDir:         flags.remoteCacheDir,
		RemoteResourceCredentials:      flags.remoteCredentials(),
		RemoteResourceGitCredentials:   flags.remoteGitCredentials(),
		OCICacheDir:                    flags.ociCacheDir,
		OCICredentials:                 flags.ociCredentials(),
		EnableAVPCompat:                flags.enableAVPCompat,
		EnableKSOPSCompat:              flags.enableKSOPSCompat,
		EnablePlugins:                  flags.enablePlugins,
		PluginCacheDir:                 flags.pluginCacheDir,
		PluginPolicyPath:               flags.pluginPolicyPath,
		PluginPolicyPathExplicit:       flags.pluginPolicyPathExplicit,
		PluginPolicyRef:                flags.pluginPolicyRef,
		PluginPolicyRepo:               flags.pluginPolicyRepo,
		DisablePluginPolicy:            flags.disablePluginPolicy,
		Parallelism:                    flags.parallelism,
		ApplicationSetProviderFixtures: append([]string(nil), flags.appsetFixtures...),
		SkipKinds:                      append([]string(nil), flags.skipKinds...),
		SkipCRDs:                       flags.skipCRDs,
		SkipSecrets:                    flags.skipSecrets,
		RecordCacheEvents:              flags.cacheEvents,
		RenderCacheEnabled:             flags.renderCache,
		RenderCacheDir:                 flags.renderCacheDir,
		RenderCacheMaxBytes:            flags.renderCacheMaxSize.bytes,
		RefreshRenders:                 flags.refreshRenders,
		EngineFingerprint:              flags.engineFingerprint,
		KubeVersion:                    flags.kubeVersion,
		APIVersions:                    append([]string(nil), flags.apiVersions...),
		NoCRDScope:                     flags.noCRDScope,
	}
}

func commandAwareFlags(cmd *cobra.Command, flags commonFlags) commonFlags {
	flags.maxDiscoveryDepthSet = cmd.Flags().Changed("max-discovery-depth")
	flags.pluginPolicyPathExplicit = cmd.Flags().Changed("plugin-policy-path")
	return flags
}

func exitCode(err error, disableDiffExitCode bool, hasDiff bool) int {
	if err != nil {
		return 2
	}
	if hasDiff && !disableDiffExitCode {
		return 1
	}
	return 0
}

func parseRepoMaps(values []string) ([]source.RepoMap, error) {
	out := make([]source.RepoMap, 0, len(values))
	for _, value := range values {
		from, to, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("repo-map %q must use URL=PATH", value)
		}
		out = append(out, source.RepoMap{
			URL:  strings.TrimSpace(from),
			Path: strings.TrimSpace(to),
		})
	}
	return out, nil
}
