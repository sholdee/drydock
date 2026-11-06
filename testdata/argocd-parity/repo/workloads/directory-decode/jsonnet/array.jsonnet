// An array output adds each element. A null element adds nothing, a List
// element is split into its items, and a List whose items are null emits
// nothing.
local configMap(name) = {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: name, namespace: 'parity-directory-decode-jsonnet' },
  data: { fixture: 'directory-decode-jsonnet' },
};

[
  null,
  configMap('parity-decode-jsonnet-array-object'),
  { apiVersion: 'v1', kind: 'List', items: [configMap('parity-decode-jsonnet-array-item')] },
  { apiVersion: 'v1', kind: 'List', items: null },
]
