function(revision) {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: 'parity-app-revision-jsonnet',
    namespace: 'parity-app-revision-jsonnet',
  },
  data: {
    revision: revision,
    short: std.extVar('short'),
    short8: std.extVar('short8'),
  },
}
