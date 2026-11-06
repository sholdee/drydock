// A List from Jsonnet is split into its items. The item has no kind, so the
// controller fails on it.
{
  apiVersion: 'v1',
  kind: 'List',
  items: [
    { data: { note: 'a jsonnet list item without a kind' } },
  ],
}
