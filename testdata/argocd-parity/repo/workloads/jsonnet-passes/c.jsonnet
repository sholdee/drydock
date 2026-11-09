{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'parity-jsonnet-passes-c' },
  data: { plain: std.extVar('plain'), code: std.extVar('code') },
}
