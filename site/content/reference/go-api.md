---
title: Go API
---

Use `github.com/sholdee/drydock/pkg/drydock` when embedding drydock's renderer
or diff pipeline in another Go program.

```go
client := drydock.NewClient(drydock.Config{
    Path: ".",
    RepoMaps: []drydock.RepoMap{
        {URL: "https://github.com/example/repo", Path: "/work/repo"},
    },
})
result, err := client.Render(ctx)
```

## API Reference

For exported types, functions, and field-level comments, use the maintained
[pkg.go.dev reference](https://pkg.go.dev/github.com/sholdee/drydock/pkg/drydock).

## Defaults

Package-level `Render`, `ListApplications`, `DiffApplications`, and
`DiffImages` functions use the same default network and cache behavior as the
CLI. Declared Git, chart, OCI artifact, and remote Kustomize inputs may be
fetched unless the request is configured for offline/cache-only behavior.

`Config.RemoteResourceCredentials` mirrors the `--remote-*` flags. Its
`Hosts` field mirrors `--remote-auth-host`, and a credential without a `Hosts`
entry fails the call before drydock fetches anything. A custom
`RemoteResourceAcquirer` receives `Hosts` and must send the credentials only
to those hosts itself.

`Config.GitCredentials` mirrors the `--git-*` flags. Its `Hosts` field
mirrors `--git-auth-host`: the default Git acquirers send `Username`,
`Password`, and `BearerToken` only to a Git URL whose scheme, host, and port
match an entry. This covers Application sources, remote Kustomize Git bases,
submodules, and the batch requests to a Git LFS server. An object download
from a Git LFS server never gets them. An HTTP Git credential without a `Hosts` entry fails the call
before drydock fetches anything. The SSH fields ignore `Hosts`. A custom
`GitAcquirer`, and a custom `RemoteResourceAcquirer` through
`RemoteResourceOptions.GitCredentials`, receives `Hosts` and must send the
HTTP credentials only to those hosts itself.

`Config.OCICacheDir` overrides the OCI artifact source cache root, mirroring
the CLI `--oci-cache-dir` flag. Like the other cache roots, it must resolve
outside current and baseline repository trees. `Config.OCICredentials`
mirrors the CLI credential flags for private OCI artifact registries, with
file material given as paths: `RegistryConfig` is the per-registry-host
credential file (the CLI feeds one `--registry-config` path to it and to
`ChartCredentials.RegistryConfig`; embedders set each), read and validated at
the first `oci://` acquisition; `Username`/`Password` are the global override
presented to every OCI artifact registry; the TLS fields are one global set
that also configures OCI Helm chart pulls. Every source whose `repoURL` has
the `oci://` scheme is an OCI artifact source, as in Argo CD's repo-server,
so `Config.ChartAcquirer` no longer receives `oci://` + `chart:` sources —
only schemeless Helm-OCI and HTTP(S) chart sources reach it. A public OCI
artifact acquirer hook is tracked as a follow-up; embedders that need a
deterministic fake for a registry-hosted chart use the schemeless shape
meanwhile.

`NewClient` accepts public Git, chart, remote-resource, and plugin renderer
interfaces so tests and embedding callers can provide deterministic fakes
without importing internal packages.

An injected `Config.ChartAcquirer` receives `Config.ChartCacheMaxAge` as
`ChartOptions.MaxAge` and is responsible for expiring its own cached charts.
A chart it returns with `ChartResult.Stale` set gets the
`source.chart-cache-stale` warning, and the render is never persisted. Under a
positive max age, persisted render outputs are served only for charts whose
`ChartResult.CacheKey` and `FetchedAt` still match an entry in drydock's chart
cache. For other charts the outputs miss with reason `chart-unverified` and
re-render.

## Results

Public render results include Applications, manifests, diagnostics, and
per-Application statuses. If one selected Application fails, `Render` returns
the error and still returns partial successful manifests, stable diagnostics,
and statuses.

`SkipKinds`, `SkipCRDs`, and `SkipSecrets` apply the same rendered-resource
filters exposed by the CLI.

## Plugins

Config management plugin sources are explicit. The CLI and default Go client
do not execute plugin commands by default.

Discovered Argo CD CMP definitions that normalize to a safe `kustomize build`
command are interpreted through drydock's native Kustomize renderer. Other
plugin sources fail closed with `plugin.unsupported` unless an embedding caller
supplies `drydock.Config.PluginRenderer` or a trusted drydock PluginPolicy
matches the plugin name or trusted static discovery for an unnamed plugin
source.

Exec and container policy require trusted provenance and `--enable-plugins`.
Application plugin parameters for command-backed engines must be allowlisted by
policy. Trusted `engine: exec` and `engine: container` policy plugins receive
the Argo CD build environment (`ARGOCD_APP_*`, `KUBE_VERSION`,
`KUBE_API_VERSIONS`) as environment variables, like a repo-server sidecar;
Application plugin env (`spec.source.plugin.env`) reaches them as
`ARGOCD_ENV_<name>` when allowlisted by `applicationEnv.allow`. Injected
`PluginRenderer` implementations are unaffected and keep receiving the full
`PluginRequest` in process. Native policy engines such as `avp-compat` and
`native-kustomize` do not execute plugin commands and reject Application
plugin env and parameters.
Explicit `argocd-vault-plugin` sources and discovered simple AVP CMP aliases
use native AVP compatibility by default. `Config.EnableAVPCompat` forces the
same placeholder redaction pass for ordinary native-rendered sources.
`Config.EnableKSOPSCompat` enables the KSOPS kustomize generator placeholder
path for repositories that use KSOPS for secret management.

Embedders can pass a renderer directly or use
`drydock.NewPluginRegistry(map[string]drydock.PluginRenderer{...})` to dispatch
in-process renderers by `plugin.name`.

The public plugin request includes the resolved source, `$ref` roots and source
metadata, kube version, and API versions.

For policy provenance, bootstrap discovery, CMP compatibility, and command
security, see [Plugin policy](/plugin-policy/).
