// shared.libsonnet is a symlink into ../targets/jsonnet.
local shared = import 'shared.libsonnet';

{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: shared.name, namespace: 'parity-directory-symlinks' },
  data: { source: 'directory-symlinks', fixture: 'jsonnet-import' },
}
