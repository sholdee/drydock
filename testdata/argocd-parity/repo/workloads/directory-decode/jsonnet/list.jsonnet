// A List from Jsonnet is split into its items, as a List in a YAML file is.
local configMap(name) = {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: name, namespace: 'parity-directory-decode-jsonnet' },
  data: { fixture: 'directory-decode-jsonnet' },
};

{
  apiVersion: 'v1',
  kind: 'List',
  items: [configMap('parity-decode-jsonnet-list-one'), configMap('parity-decode-jsonnet-list-two')],
}
