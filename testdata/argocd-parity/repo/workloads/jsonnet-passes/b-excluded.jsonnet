{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'parity-jsonnet-passes-b-excluded' },
  data: { plain: std.extVar('plain'), code: std.extVar('code') },
}
