# Config management plugin program for the parity-decode-plugin-empty-object
# case. The parity-env sidecar and drydock's exec engine both run it. It
# prints a ConfigMap and an empty object. kube.SplitYAML keeps the empty
# object and fails because it has no kind.
BEGIN {
  print "apiVersion: v1"
  print "kind: ConfigMap"
  print "metadata:"
  print "  name: parity-decode-plugin-empty-object"
  print "  namespace: parity-directory-decode-workloads"
  print "data:"
  print "  fixture: directory-decode"
  print "---"
  print "{}"
}
