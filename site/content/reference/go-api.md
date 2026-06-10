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
CLI. Declared Git, chart, and remote Kustomize inputs may be fetched unless
the request is configured for offline/cache-only behavior.

`NewClient` accepts public Git, chart, remote-resource, and plugin renderer
interfaces so tests and embedding callers can provide deterministic fakes
without importing internal packages.

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
policy. Native policy engines such as `avp-compat` and `native-kustomize` do
not execute plugin commands and reject Application plugin env and parameters.
Explicit `argocd-vault-plugin` sources and discovered simple AVP CMP aliases
use native AVP compatibility by default. `Config.EnableAVPCompat` forces the
same placeholder redaction pass for ordinary native-rendered sources.

Embedders can pass a renderer directly or use
`drydock.NewPluginRegistry(map[string]drydock.PluginRenderer{...})` to dispatch
in-process renderers by `plugin.name`.

The public plugin request includes the resolved source, `$ref` roots and source
metadata, kube version, and API versions.

For policy provenance, bootstrap discovery, CMP compatibility, and command
security, see [Plugin policy](/plugin-policy/).
