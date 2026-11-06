// An object needs a kind but not an apiVersion, so Argo CD renders it without
// an apiVersion.
{
  kind: 'ConfigMap',
  metadata: { name: 'parity-decode-jsonnet-kind-only', namespace: 'parity-directory-decode-jsonnet' },
  data: { fixture: 'directory-decode-jsonnet' },
}
