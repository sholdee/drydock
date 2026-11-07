// With Jsonnet off, the directory render skips this file.
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'parity-gates-jsonnet-evaluated', namespace: 'parity-source-type-gates' },
  data: { note: 'evaluated' },
}
