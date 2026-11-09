{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'parity-jsonnet-passes-a' },
  data: { plain: std.extVar('plain'), code: std.extVar('code'), deep: std.extVar('deep') },
}
