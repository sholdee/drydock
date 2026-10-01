# Config management plugin program for the container-plugin parity fixture.
#
# Both sides run it with busybox awk inside the same pinned alpine image: the
# live repo-server CMP sidecar, and drydock's container engine through
# `docker run`. Each emitted key proves one leg of the container contract:
# alpineRelease that the plugin ran inside the pinned image (neither the
# argocd image nor the host has /etc/alpine-release), greeting that the
# Application source is the working directory (drydock bind-mounts its copy at
# /work), and the rest that the Application env and parameters crossed the
# container boundary. A missing file is a hard failure rather than an empty
# value, so both sides failing the same way cannot compare equal.

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

# first_line returns the first line of file, or exits non-zero when the file
# is missing or empty.
function first_line(file,    line) {
  if ((getline line < file) <= 0 || line == "") {
    print "generate.awk: cannot read " file > "/dev/stderr"
    exit 1
  }
  close(file)
  return line
}

BEGIN {
  # Read both files before printing anything, so a failure emits no partial
  # manifest on stdout.
  alpine_release = first_line("/etc/alpine-release")
  greeting = first_line("greeting.txt")

  print "apiVersion: v1"
  print "kind: ConfigMap"
  print "metadata:"
  print "  name: \"parity-plugin-container\""
  printf "  namespace: \"%s\"\n", esc(ENVIRON["ARGOCD_APP_NAMESPACE"])
  print "data:"
  emit("alpineRelease", alpine_release)
  emit("greeting", greeting)
  emit("appName", ENVIRON["ARGOCD_APP_NAME"])
  emit("appNamespace", ENVIRON["ARGOCD_APP_NAMESPACE"])
  emit("envMode", ENVIRON["ARGOCD_ENV_MODE"])
  emit("paramTitle", ENVIRON["PARAM_TITLE"])
}
