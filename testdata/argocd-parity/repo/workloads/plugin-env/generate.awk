# Config management plugin program for the Argo CD render parity fixture.
#
# The same file runs on both sides of the comparison: the live repo-server CMP
# sidecar (mawk, in the quay.io/argoproj/argocd image) and drydock's exec
# engine (BSD awk on macOS, mawk on ubuntu-latest). Hence POSIX only: BEGIN
# block, no getline, no gensub, no /dev/stdin. It prints ONLY the values that
# are comparable between the two sides; the fixture README lists what is
# excluded and why.

# esc quotes a value for a YAML double-quoted scalar. Only backslash and the
# double quote need escaping; every fixture value is printable ASCII.
function esc(value,    out, index_, char) {
  out = ""
  for (index_ = 1; index_ <= length(value); index_++) {
    char = substr(value, index_, 1)
    if (char == "\\") {
      out = out "\\\\"
    } else if (char == "\"") {
      out = out "\\\""
    } else {
      out = out char
    }
  }
  return out
}

function emit(key, value) {
  printf "  %s: \"%s\"\n", key, esc(value)
}

BEGIN {
  # Derive these two before any ENVIRON[name] lookup: a lookup of a missing key
  # creates that key in some awks, which would corrupt the PARAM_ count.
  bare_mode = ("MODE" in ENVIRON) ? "set" : "unset"
  param_count = 0
  for (name in ENVIRON) {
    if (substr(name, 1, 6) == "PARAM_") {
      param_count++
    }
  }

  print "apiVersion: v1"
  print "kind: ConfigMap"
  print "metadata:"
  print "  name: parity-plugin-env"
  printf "  namespace: %s\n", ENVIRON["ARGOCD_APP_NAMESPACE"]
  print "data:"
  emit("appName", ENVIRON["ARGOCD_APP_NAME"])
  emit("appNamespace", ENVIRON["ARGOCD_APP_NAMESPACE"])
  emit("projectName", ENVIRON["ARGOCD_APP_PROJECT_NAME"])
  emit("sourceRepoUrl", ENVIRON["ARGOCD_APP_SOURCE_REPO_URL"])
  emit("sourcePath", ENVIRON["ARGOCD_APP_SOURCE_PATH"])
  emit("sourceTargetRevision", ENVIRON["ARGOCD_APP_SOURCE_TARGET_REVISION"])
  emit("envMode", ENVIRON["ARGOCD_ENV_MODE"])
  emit("envSuffix", ENVIRON["ARGOCD_ENV_SUFFIX"])
  emit("parameters", ENVIRON["ARGOCD_APP_PARAMETERS"])
  emit("paramTitle", ENVIRON["PARAM_TITLE"])
  emit("paramItems0", ENVIRON["PARAM_ITEMS_0"])
  emit("paramItems1", ENVIRON["PARAM_ITEMS_1"])
  # bareMode pins the ARGOCD_ENV_ prefix rule: the Application's plugin env
  # entry must never arrive under its bare name.
  emit("bareMode", bare_mode)
  # paramCount pins the PARAM_ surface: exactly the three names the two
  # declared parameters produce, and nothing else.
  emit("paramCount", param_count "")
}
