{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: std.extVar('name'),
    namespace: std.extVar('namespace'),
  },
  data: {
    source: 'jsonnet',
  },
}
