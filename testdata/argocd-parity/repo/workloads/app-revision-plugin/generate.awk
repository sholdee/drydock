# Config management plugin program for the ARGOCD_APP_REVISION parity case.
#
# Like workloads/plugin-env/generate.awk it runs on both sides (the live CMP
# sidecar and drydock's exec engine), so it is POSIX awk with a BEGIN block
# only. It prints the three revision variables of the build environment.
# Every fixture value is a hex SHA, so no quoting is needed.

BEGIN {
  print "apiVersion: v1"
  print "kind: ConfigMap"
  print "metadata:"
  print "  name: parity-app-revision-plugin"
  printf "  namespace: %s\n", ENVIRON["ARGOCD_APP_NAMESPACE"]
  print "data:"
  printf "  revision: \"%s\"\n", ENVIRON["ARGOCD_APP_REVISION"]
  printf "  short: \"%s\"\n", ENVIRON["ARGOCD_APP_REVISION_SHORT"]
  printf "  short8: \"%s\"\n", ENVIRON["ARGOCD_APP_REVISION_SHORT_8"]
  printf "  targetRevision: \"%s\"\n", ENVIRON["ARGOCD_APP_SOURCE_TARGET_REVISION"]
}
