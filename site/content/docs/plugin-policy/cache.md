---
title: Container Plugin Caches
---

Container `cacheMounts` let trusted plugins keep durable tool caches without
granting policy authors a host path escape hatch.

Each cache mount names a cache and chooses only a container target path under
reserved `/drydock-cache`, not `/drydock-cache` itself. drydock selects the host
cache directory under its user cache root and scopes it by policy fingerprint,
plugin name, and cache name.

Targets cannot overlap `/work`, contain traversal, commas, control characters,
backslashes, duplicate paths, or ancestor/descendant overlaps.

`--plugin-cache-dir PATH` overrides the host root for these policy-managed
container plugin cache mounts at render time. It does not change the trusted
policy target paths, and it does not make plugin caches part of `drydock cache`
lifecycle commands; those commands still manage only Git, chart, and
remote-resource cache entry roots for now.

The GitHub PR action sets the plugin cache directory under the action cache
root, so trusted plugin cache mounts can be restored and saved with the render
cache when trusted plugins are enabled.

Use `network: default` only when trusted package resolution needs network
access. It is rejected with `--offline`.
