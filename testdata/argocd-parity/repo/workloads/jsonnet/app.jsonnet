function(namespace) {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: std.extVar('name'),
    namespace: namespace,
  },
  data: {
    source: 'jsonnet',
    fixture: 'argocd-parity',
  },
}
