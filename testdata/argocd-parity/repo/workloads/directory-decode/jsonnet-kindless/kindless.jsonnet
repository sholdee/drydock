// The repo-server decodes Jsonnet output into Unstructured objects, and that
// decode needs a kind. So this object fails the source in the repo-server.
{
  name: 'not-a-manifest',
  version: '1.0.0',
}
