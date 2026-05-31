{
  library: 'repo-relative',
  configMap(name, namespace, data):: {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: {
      name: name,
      namespace: namespace,
    },
    data: data,
  },
}
