// Reached through app/linked.jsonnet. The relative import resolves against
// the directory of the symlink, so app/lib.libsonnet wins over the
// lib.libsonnet next to this file.
local lib = import 'lib.libsonnet';

{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: lib.name, namespace: 'parity-directory-symlinks' },
  data: { source: 'directory-symlinks', fixture: 'jsonnet-entry' },
}
