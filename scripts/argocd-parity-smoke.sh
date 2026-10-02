#!/usr/bin/env bash
set -euo pipefail

# The OCI registry bridge needs an /etc/hosts entry for
# argocd-parity-registry.argocd-parity.svc.cluster.local. The script appends
# it with sudo (passwordless on CI; an interactive prompt locally) and removes
# its own marked line on exit. A pre-existing entry for that hostname —
# however it got there — skips sudo entirely, which is the local escape hatch
# for environments without sudo. On macOS the bridge uses local port 5443
# (AirPlay owns 5000) and certificate generation sticks to the
# LibreSSL-compatible config-file subjectAltName form.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ARGOCD_MODULE="github.com/argoproj/argo-cd/v3"
FIXTURE_REPO_URL="git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git"
FIXTURE_REPO_PATH="${REPO_ROOT}/testdata/argocd-parity/repo"
IGNORE_FILE="${REPO_ROOT}/testdata/argocd-parity/compare-ignore.yaml"
SIDECAR_PATH="${REPO_ROOT}/testdata/argocd-parity/sidecar"
PROJECT_POLICY_REPO_PATH="${REPO_ROOT}/testdata/argocd-project-policy/repo"
PROJECT_POLICY_EXPECTED="${REPO_ROOT}/testdata/argocd-project-policy/expected.yaml"
OCI_ARTIFACT_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-artifact"
OCI_REGISTRY_HOST="argocd-parity-registry.argocd-parity.svc.cluster.local"
OCI_REGISTRY_PORT="5443"
# Multi-arch index digest for the Docker-official registry:2.8.3 (covers CI
# amd64 and local darwin arm64). Pulls try each mirror in order, retrying with
# backoff; every mirror is pinned to the same digest, so all serve identical
# content. The ECR mirror comes first (no Docker Hub 429 surface), but its
# anonymous quota is per egress IP and CI runners share IPs, so it can answer
# "toomanyrequests: Data limit exceeded" for a whole job.
OCI_REGISTRY_IMAGE_DIGEST="sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
OCI_REGISTRY_IMAGE_REPOSITORIES=(
  "public.ecr.aws/docker/library/registry"
  "mirror.gcr.io/library/registry"
  "docker.io/library/registry"
)
PULL_RETRY_ATTEMPTS=4
PULL_RETRY_INITIAL_DELAY_SECONDS=5
# Multi-arch index digest for the Docker-official alpine:3.23.6, identical on
# every mirror below. The container-plugin fixture runs it twice: as the
# repo-server CMP sidecar image (re-tagged and kind-loaded) and as drydock's
# container-engine image. The first mirror is the reference the committed
# policy (testdata/argocd-parity/repo/.drydock/plugins.yaml) names.
PARITY_ALPINE_IMAGE_DIGEST="sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0"
PARITY_ALPINE_IMAGE_REPOSITORIES=(
  "public.ecr.aws/docker/library/alpine"
  "mirror.gcr.io/library/alpine"
  "docker.io/library/alpine"
)
# Set by prepare_parity_alpine_image: the digest reference the pull actually
# succeeded with, which is the only name the local image store has for it.
PARITY_ALPINE_POLICY_IMAGE=""
CONTAINER_PLUGIN_APPLICATION="parity-plugin-container"
# argocd-vault-plugin release binary for the AVP sidecar, built into an image
# on top of the pinned alpine above. Release binaries are static (CGO off),
# so they run on alpine's musl. Pinned per architecture by sha256.
AVP_VERSION="v1.18.1"
AVP_SHA256_LINUX_AMD64="9e8e301c0d4e01f050b4df1e47a4137eb0ba459944ed2c32b53ef1571eb93c40"
AVP_SHA256_LINUX_ARM64="dd6e4d7db290c2f16aeb142941cd727e623b3faa25258c7743d9ddfac39db1c3"
AVP_APPLICATION="parity-avp"
# The Secrets-only AVP Application. `argocd app manifests` masks Secret data
# and stringData as ++++++++, so it is kept out of the masked comparison and
# compared through the kubectl-exec oracle alone.
AVP_SECRET_APPLICATION="parity-avp-secret"
# Applications the oracle renders with `argocd-vault-plugin generate` inside
# the sidecar and compares unmasked against drydock.
AVP_ORACLE_APPLICATIONS=("${AVP_APPLICATION}" "${AVP_SECRET_APPLICATION}")
# drydock-redacted- markers testdata/argocd-parity/repo/workloads/avp must
# render on each side: one per substituted placeholder occurrence (the two
# inside the nested list stay verbatim).
AVP_EXPECTED_MARKERS=16
# Markers testdata/argocd-parity/repo/workloads/avp-secret must render on
# each oracle side: four literal in stringData plus five inside decoded
# base64 data values.
AVP_SECRET_EXPECTED_MARKERS=9
OCI_ARTIFACT_REPOSITORY="parity/config"
OCI_ARTIFACT_TAG="v1.0.0"
OCI_APPLICATION="parity-oci-config"
# The OCI Helm chart fixture: a nested chart name under a scheme-less
# host:port repoURL, the shape Argo CD dispatches to `helm pull
# oci://<repo>/<chart>` and drydock now accepts too.
OCI_CHART_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-chart/parity-nested-chart"
OCI_CHART_REPOSITORY="parity/nested"
OCI_CHART_APPLICATION="parity-oci-helm-nested"
OCI_HOSTS_MARKER="drydock-argocd-parity-smoke"
HOSTS_ENTRY_ADDED="false"
OUT_DIR="${REPO_ROOT}/argocd-parity-smoke"
KEEP_CLUSTER="false"
CREATE_CLUSTER="true"
RUN_PROJECT_POLICY_SMOKE="true"
CLUSTER_NAME="drydock-argocd-parity-${GITHUB_RUN_ID:-$$}"
DRYDOCK_CMD=(go run ./cmd/drydock)
PORT_FORWARD_PID=""
REGISTRY_PORT_FORWARD_PID=""

APPLICATIONS=(
  parity-oci-config
  parity-oci-helm-nested
  parity-directory
  parity-directory-edges
  parity-helm-release-namespace
  parity-helm-capabilities
  parity-helm-file-parameters
  parity-helm-parameters
  parity-helm-render-options
  parity-helm-values
  parity-helm-valuefiles-glob
  parity-jsonnet
  parity-jsonnet-edges
  parity-kustomize
  parity-kustomize-helm
  parity-multi-source-ref-values
  parity-ref-only-source
  parity-repeated-resource
  parity-skip-file
  parity-sources-precedence
  parity-tracking
  parity-git-alpha
  parity-git-beta
  parity-git-file-alpha
  parity-git-file-beta
  parity-list-alpha
  parity-list-beta
  parity-merge-alpha
  parity-merge-beta
  parity-matrix-dev-api
  parity-matrix-dev-worker
  parity-matrix-prod-api
  parity-matrix-prod-worker
  parity-multi-source-last-wins
  parity-kustomize-options
  parity-selector-beta-prod
  parity-template-patch
  parity-helm-subchart
  parity-helm-crds-default
  parity-helm-values-precedence
  parity-helm-options-edge
  parity-source-overrides
  parity-ref-fileparam
  parity-helm-params-edge
  parity-directory-flat
  parity-directory-forced
  parity-directory-glob
  parity-kustomize-variants
  parity-kustomize-labels
  parity-kustomize-generators
  parity-kustomize-helm-capabilities
  parity-kustomize-helm-cross-namespace
  parity-ft-alpha
  parity-ft-beta
  parity-fn-gamma-one
  parity-helm-null-default
  parity-helm-block-scalars
  parity-plugin-env
  parity-plugin-container
  parity-avp
  parity-avp-secret
)

TRACKING_APPLICATIONS=(
  parity-tracking
  parity-helm-crds-default
  parity-oci-config
  parity-tenant-overrides
  parity-plugin-env
)

# Applications outside the Argo CD controller namespace. They pin instance-name
# semantics: ARGOCD_APP_NAME is <namespace>_<name>, the per-Application source
# override file is .argocd-source-<namespace>_<name>.yaml (the bare-name file is
# ignored), and tracking metadata carries the same instance name.
TENANT_NAMESPACE="parity-tenant"
TENANT_APPLICATIONS=(
  parity-tenant-overrides
)

# Applications rendered through a config management plugin. Their drydock
# capture carries --enable-plugins plus trusted policy provenance; no other
# app may, because --plugin-policy-ref makes a missing policy fatal and
# materializes the whole policy-repo tree per invocation.
PLUGIN_APPLICATIONS=(
  parity-plugin-env
  parity-plugin-container
)
# CMP sidecars patched onto argocd-repo-server. Each entry is both the
# container name and the ConfigManagementPlugin name, so its socket is
# /home/argocd/cmp-server/plugins/<entry>.sock.
CMP_SIDECARS=(
  parity-env
  parity-container
  argocd-vault-plugin
)
# Set by prepare_fixture_git_image: the local git repo the smoke builds from
# the working tree. It is the trusted policy repo for the plugin capture, so
# a local run with an uncommitted fixture is trusted exactly like CI's PR
# merge commit.
FIXTURE_GIT_WORK=""

PROJECT_POLICY_CASES=(
  "argocd|project-policy-source-allowed|none"
  "argocd|project-policy-source-denied|source"
  "argocd|project-policy-destination-allowed|none"
  "argocd|project-policy-destination-denied|destination"
  "project-policy-tenant|project-policy-source-namespace-allowed|none"
  "project-policy-tenant|project-policy-source-namespace-denied|source-namespace"
)

usage() {
  cat <<'USAGE'
Usage: scripts/argocd-parity-smoke.sh [options]

Options:
  --binary <path>        drydock binary to run instead of go run ./cmd/drydock
  --out <dir>           output artifact directory (default: ./argocd-parity-smoke)
  --cluster-name <name> kind cluster name
  --existing-cluster    use an already-created kind cluster with --cluster-name
  --skip-project-policy-smoke
                        skip default project-policy smoke after parity checks
  --keep-cluster        leave the kind cluster running for debugging
  -h, --help            show this help
USAGE
}

fail() {
  echo "argocd render parity smoke: $*" >&2
  exit 2
}

log_step() {
  echo "==> $*" >&2
}

# retry runs a command until it succeeds, at most PULL_RETRY_ATTEMPTS times,
# sleeping PULL_RETRY_INITIAL_DELAY_SECONDS and then doubling between attempts.
# It is for downloads: image pulls, which fail transiently under anonymous
# rate limits, and the AVP release binary.
retry() {
  local description="$1" attempt=1 delay="${PULL_RETRY_INITIAL_DELAY_SECONDS}"
  shift
  until "$@"; do
    if ((attempt >= PULL_RETRY_ATTEMPTS)); then
      return 1
    fi
    echo "argocd render parity smoke: ${description} failed (attempt ${attempt}/${PULL_RETRY_ATTEMPTS}); retrying in ${delay}s" >&2
    sleep "${delay}"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}

require_value() {
  local flag="$1"
  local value="${2:-}"
  [[ -n "${value}" ]] || fail "${flag} requires a value"
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --binary)
      require_value "$1" "${2:-}"
      DRYDOCK_CMD=("$2")
      shift 2
      ;;
    --out)
      require_value "$1" "${2:-}"
      OUT_DIR="$2"
      shift 2
      ;;
    --cluster-name)
      require_value "$1" "${2:-}"
      CLUSTER_NAME="$2"
      shift 2
      ;;
    --existing-cluster)
      CREATE_CLUSTER="false"
      shift
      ;;
    --skip-project-policy-smoke)
      RUN_PROJECT_POLICY_SMOKE="false"
      shift
      ;;
    --keep-cluster)
      KEEP_CLUSTER="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "required tool not found: $1"
}

for tool in base64 curl docker git go kind kubectl openssl oras; do
  require_tool "${tool}"
done

[[ -d "${FIXTURE_REPO_PATH}" ]] || fail "fixture repo not found: ${FIXTURE_REPO_PATH}"
[[ -f "${IGNORE_FILE}" ]] || fail "compare ignore file not found: ${IGNORE_FILE}"
[[ -d "${OCI_ARTIFACT_PATH}" ]] || fail "OCI artifact content directory not found: ${OCI_ARTIFACT_PATH}"
[[ -f "${OCI_CHART_PATH}/Chart.yaml" ]] || fail "OCI Helm chart fixture not found: ${OCI_CHART_PATH}/Chart.yaml"
[[ -d "${PROJECT_POLICY_REPO_PATH}" ]] || fail "project policy fixture repo not found: ${PROJECT_POLICY_REPO_PATH}"
[[ -f "${PROJECT_POLICY_EXPECTED}" ]] || fail "project policy expected file not found: ${PROJECT_POLICY_EXPECTED}"
[[ -d "${SIDECAR_PATH}" ]] || fail "CMP sidecar manifest directory not found: ${SIDECAR_PATH}"

OUT_DIR="$(mkdir -p "${OUT_DIR}" && cd "${OUT_DIR}" && pwd)"
WORK_DIR="$(mktemp -d)"
ARGOCD_CONFIG_DIR="${WORK_DIR}/argocd"
ARGOCD_CONFIG="${ARGOCD_CONFIG_DIR}/config"
mkdir -p "${ARGOCD_CONFIG_DIR}"
chmod 0700 "${ARGOCD_CONFIG_DIR}"
export ARGOCD_CONFIG

# The CA file is re-read at every drydock OCI client construction, so it must
# outlive every drydock invocation: it lives in WORK_DIR (trap lifetime), and
# the warm run and the offline per-app loop share these exact variables.
OCI_TLS_DIR="${WORK_DIR}/registry-tls"
OCI_CA_FILE="${OCI_TLS_DIR}/tls.crt"
OCI_TLS_KEY_FILE="${OCI_TLS_DIR}/tls.key"
OCI_CACHE_DIR="${WORK_DIR}/oci-cache"
# OCI Helm charts land in the chart cache, not the artifact cache: the warm
# run fills it and the offline per-app loop reads it back.
CHART_CACHE_DIR="${WORK_DIR}/chart-cache"
# Fresh, non-overlapping render cache dirs: the persistent render cache key
# omits Offline, so sharing one dir would let the offline loop replay the
# warm (non-offline) render instead of exercising offline resolve+extract.
OCI_WARM_RENDER_CACHE_DIR="${WORK_DIR}/render-cache-warm"
OCI_OFFLINE_RENDER_CACHE_DIR="${WORK_DIR}/render-cache-offline"

cleanup() {
  local status="$?"
  local pid
  for pid in "${PORT_FORWARD_PID}" "${REGISTRY_PORT_FORWARD_PID}"; do
    if [[ -n "${pid}" ]]; then
      kill "${pid}" >/dev/null 2>&1 || true
    fi
  done
  if [[ "${HOSTS_ENTRY_ADDED}" == "true" ]] && sudo -n true 2>/dev/null; then
    # Best-effort removal of only the marked line this script appended.
    sudo -n sed -i".${OCI_HOSTS_MARKER}.bak" "/# ${OCI_HOSTS_MARKER}\$/d" /etc/hosts >/dev/null 2>&1 || true
    sudo -n rm -f "/etc/hosts.${OCI_HOSTS_MARKER}.bak" >/dev/null 2>&1 || true
  fi
  if [[ "${status}" -ne 0 ]]; then
    collect_logs || true
  fi
  rm -rf "${WORK_DIR}"
  if [[ "${CREATE_CLUSTER}" == "true" && "${KEEP_CLUSTER}" != "true" ]]; then
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

artifact_dir() {
  mkdir -p "${OUT_DIR}/$1"
  printf '%s\n' "${OUT_DIR}/$1"
}

collect_logs() {
  local logs_dir sidecar
  logs_dir="$(artifact_dir logs)"
  # repo-server carries the parity CMP sidecars; name each container
  # explicitly so every log lands under its own filename regardless of the
  # default-container annotation.
  kubectl -n argocd logs deployment/argocd-repo-server -c argocd-repo-server --tail=300 > "${logs_dir}/argocd-repo-server.log" 2>&1 || true
  for sidecar in "${CMP_SIDECARS[@]}"; do
    kubectl -n argocd logs deployment/argocd-repo-server -c "${sidecar}" --tail=300 > "${logs_dir}/argocd-repo-server-${sidecar}.log" 2>&1 || true
  done
  kubectl -n argocd logs statefulset/argocd-application-controller --tail=300 > "${logs_dir}/argocd-application-controller.log" 2>&1 || true
  kubectl -n argocd logs deployment/argocd-applicationset-controller --tail=300 > "${logs_dir}/argocd-applicationset-controller.log" 2>&1 || true
  kubectl -n argocd-parity logs deployment/argocd-parity-registry --tail=300 > "${logs_dir}/argocd-parity-registry.log" 2>&1 || true
}

resolve_argocd_version() {
  local version
  version="$(cd "${REPO_ROOT}" && go list -m -f '{{.Version}}' "${ARGOCD_MODULE}")"
  [[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]] || fail "could not resolve concrete Argo CD version from ${ARGOCD_MODULE}: ${version}"
  printf '%s\n' "${version}"
}

install_argocd_cli() {
  local version="$1"
  local os arch url target
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "${arch}" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) fail "unsupported argocd CLI architecture: ${arch}" ;;
  esac
  target="${WORK_DIR}/bin/argocd"
  mkdir -p "${WORK_DIR}/bin"
  url="https://github.com/argoproj/argo-cd/releases/download/${version}/argocd-${os}-${arch}"
  curl -fsSL "${url}" -o "${target}"
  chmod 0755 "${target}"
  export PATH="${WORK_DIR}/bin:${PATH}"
}

# pin_container_policy_image points the container plugin policy in the given
# file at the reference the alpine pull succeeded with. drydock runs the image
# with `--pull never`, and a digest pull from a fallback mirror leaves the
# image under that mirror's name only. Only the git snapshot is rewritten:
# drydock reads trusted policy from --plugin-policy-repo at
# --plugin-policy-ref, never from --path. The check runs even when nothing was
# rewritten, so a digest bumped in the script but not in the committed policy
# (or the reverse) fails here instead of as an offline image miss.
pin_container_policy_image() {
  local policy="$1"
  local canonical content
  canonical="${PARITY_ALPINE_IMAGE_REPOSITORIES[0]}@${PARITY_ALPINE_IMAGE_DIGEST}"
  [[ -n "${PARITY_ALPINE_POLICY_IMAGE}" ]] \
    || fail "PARITY_ALPINE_POLICY_IMAGE is unset; prepare_parity_alpine_image must run before prepare_fixture_git_image"
  [[ -f "${policy}" ]] || fail "plugin policy not found in the fixture git snapshot: ${policy}"
  if [[ "${PARITY_ALPINE_POLICY_IMAGE}" != "${canonical}" ]]; then
    content="$(< "${policy}")"
    # The replacement stays unquoted: bash 3.2 would keep the quotes
    # literally, and an image reference carries no & or backslash that bash
    # 5.2's patsub_replacement would expand.
    printf '%s\n' "${content//"${canonical}"/${PARITY_ALPINE_POLICY_IMAGE}}" > "${policy}" \
      || fail "could not rewrite the container plugin image in ${policy}"
    echo "argocd render parity smoke: container plugin policy image rewritten to the pulled mirror reference ${PARITY_ALPINE_POLICY_IMAGE}" >&2
  fi
  grep -qF "image: ${PARITY_ALPINE_POLICY_IMAGE}" "${policy}" \
    || fail "container plugin policy ${policy} does not name the pulled image ${PARITY_ALPINE_POLICY_IMAGE}; keep its image digest in sync with PARITY_ALPINE_IMAGE_DIGEST"
}

prepare_fixture_git_image() {
  local bare image dockerfile
  # Script-scoped: this repo is also the trusted plugin policy repo for the
  # plugin Application's drydock capture.
  FIXTURE_GIT_WORK="${WORK_DIR}/fixture-src"
  bare="${WORK_DIR}/repo.git"
  image="drydock-argocd-parity-git:${CLUSTER_NAME}"
  mkdir -p "${FIXTURE_GIT_WORK}"
  cp -R "${FIXTURE_REPO_PATH}/." "${FIXTURE_GIT_WORK}/"
  cp -R "${PROJECT_POLICY_REPO_PATH}/." "${FIXTURE_GIT_WORK}/"
  pin_container_policy_image "${FIXTURE_GIT_WORK}/.drydock/plugins.yaml"
  git -C "${FIXTURE_GIT_WORK}" init --initial-branch=main >/dev/null
  git -C "${FIXTURE_GIT_WORK}" config user.email "drydock@example.invalid"
  git -C "${FIXTURE_GIT_WORK}" config user.name "drydock render parity smoke"
  git -C "${FIXTURE_GIT_WORK}" add .
  git -C "${FIXTURE_GIT_WORK}" commit -m "seed argocd parity fixture" >/dev/null
  git clone --bare --no-hardlinks "${FIXTURE_GIT_WORK}" "${bare}" >/dev/null
  git --git-dir="${bare}" update-server-info

  dockerfile="${WORK_DIR}/Dockerfile.git"
  cat > "${dockerfile}" <<'DOCKERFILE'
FROM alpine:3.23
RUN apk add --no-cache git-daemon
COPY --chown=65534:65534 repo.git /srv/git/repo.git
RUN touch /srv/git/repo.git/git-daemon-export-ok && chmod -R a+rX /srv/git
EXPOSE 9418
USER 65534:65534
ENTRYPOINT ["git", "daemon", "--verbose", "--export-all", "--base-path=/srv/git", "--reuseaddr", "--informative-errors", "/srv/git"]
DOCKERFILE
  # The build pulls its base image from Docker Hub and packages from the
  # Alpine CDN, both rate-limited for anonymous clients.
  retry "docker build of ${image}" docker build -q -t "${image}" -f "${dockerfile}" "${WORK_DIR}" >/dev/null \
    || fail "docker build of the fixture Git server image ${image} failed"
  kind load docker-image "${image}" --name "${CLUSTER_NAME}"
}

install_fixture_git_server() {
  kubectl create namespace argocd-parity >/dev/null
  kubectl -n argocd-parity apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-parity-git
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: argocd-parity-git
  template:
    metadata:
      labels:
        app.kubernetes.io/name: argocd-parity-git
    spec:
      containers:
        - name: git
          image: drydock-argocd-parity-git:${CLUSTER_NAME}
          imagePullPolicy: Never
          ports:
            - containerPort: 9418
              name: git
---
apiVersion: v1
kind: Service
metadata:
  name: argocd-parity-git
spec:
  selector:
    app.kubernetes.io/name: argocd-parity-git
  ports:
    - name: git
      port: 9418
      targetPort: git
YAML
  kubectl -n argocd-parity rollout status deployment/argocd-parity-git --timeout=120s
}

ensure_registry_hosts_entry() {
  if grep -qF "${OCI_REGISTRY_HOST}" /etc/hosts; then
    return 0
  fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    sudo -n true 2>/dev/null \
      || fail "passwordless sudo is required on CI to append the ${OCI_REGISTRY_HOST} entry to /etc/hosts"
  else
    echo "sudo is required to append '127.0.0.1 ${OCI_REGISTRY_HOST}' to /etc/hosts (add that entry manually beforehand to skip sudo)" >&2
    sudo -v \
      || fail "sudo credentials are required to append the ${OCI_REGISTRY_HOST} entry to /etc/hosts; add '127.0.0.1 ${OCI_REGISTRY_HOST}' manually and rerun to skip sudo"
  fi
  printf '127.0.0.1 %s # %s\n' "${OCI_REGISTRY_HOST}" "${OCI_HOSTS_MARKER}" | sudo tee -a /etc/hosts >/dev/null \
    || fail "could not append the ${OCI_REGISTRY_HOST} entry to /etc/hosts"
  HOSTS_ENTRY_ADDED="true"
}

generate_registry_certificate() {
  local config="${OCI_TLS_DIR}/openssl.cnf"
  mkdir -p "${OCI_TLS_DIR}"
  # Config-file subjectAltName form: works on LibreSSL (macOS /usr/bin/openssl)
  # and OpenSSL 3.x alike; -addext does not. CA:TRUE + keyCertSign let Go
  # clients (Argo CD repo-server and drydock) use the self-signed leaf as its
  # own trust root.
  cat > "${config}" <<CONFIG
[req]
distinguished_name = dn
x509_extensions = v3_req
prompt = no

[dn]
CN = ${OCI_REGISTRY_HOST}

[v3_req]
subjectAltName = DNS:${OCI_REGISTRY_HOST}
basicConstraints = critical, CA:TRUE
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign
extendedKeyUsage = serverAuth
CONFIG
  openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3 \
    -keyout "${OCI_TLS_KEY_FILE}" -out "${OCI_CA_FILE}" \
    -config "${config}" >/dev/null 2> "${OUT_DIR}/openssl-cert.stderr" \
    || fail "openssl self-signed certificate generation for ${OCI_REGISTRY_HOST} failed; see ${OUT_DIR}/openssl-cert.stderr"
  rm -f "${OUT_DIR}/openssl-cert.stderr"
  [[ -s "${OCI_CA_FILE}" && -s "${OCI_TLS_KEY_FILE}" ]] \
    || fail "generated OCI registry TLS certificate or key is missing or empty under ${OCI_TLS_DIR}"
}

# pull_pinned_image pulls <digest> from the first of the given repositories
# that serves it and prints the reference it pulled.
pull_pinned_image() {
  local digest="$1"
  shift
  local repository reference
  for repository in "$@"; do
    reference="${repository}@${digest}"
    if retry "docker pull of ${reference}" docker pull "${reference}" >/dev/null; then
      printf '%s\n' "${reference}"
      return 0
    fi
    echo "argocd render parity smoke: giving up on ${repository}; trying the next mirror" >&2
  done
  return 1
}

# daemon_linux_arch prints the architecture of the Docker daemon, which is the
# architecture of every image it builds and of the kind node it runs. The
# shell's own architecture can differ (a Rosetta shell, a remote DOCKER_HOST).
daemon_linux_arch() {
  local arch
  arch="$(docker version --format '{{.Server.Arch}}')" \
    || fail "docker version failed; is the Docker daemon reachable?"
  case "${arch}" in
    amd64 | arm64) printf '%s\n' "${arch}" ;;
    *) fail "unsupported Docker daemon architecture: ${arch}" ;;
  esac
}

# kind_load_image loads a local image into the kind cluster, falling back to a
# single-platform export.
kind_load_image() {
  local image="$1"
  local description="$2"
  local arch archive
  if kind load docker-image "${image}" --name "${CLUSTER_NAME}"; then
    return 0
  fi
  # Under Docker's containerd image store a digest pull keeps the multi-arch
  # index but only the host platform's blobs; kind's `ctr images import
  # --all-platforms` of the docker-save stream then fails on the missing
  # foreign-platform manifests ("content digest ...: not found"). Exporting
  # just the host platform sidesteps the index entirely.
  arch="$(daemon_linux_arch)"
  archive="${WORK_DIR}/kind-load-${description// /-}.tar"
  docker save --platform "linux/${arch}" "${image}" -o "${archive}" \
    || fail "single-platform docker save of the ${description} image ${image} for linux/${arch} failed"
  kind load image-archive "${archive}" --name "${CLUSTER_NAME}" \
    || fail "kind load of the ${description} image ${image} into cluster ${CLUSTER_NAME} failed"
  rm -f "${archive}"
}

prepare_registry_image() {
  local image="drydock-argocd-parity-registry:${CLUSTER_NAME}"
  local pulled
  pulled="$(pull_pinned_image "${OCI_REGISTRY_IMAGE_DIGEST}" "${OCI_REGISTRY_IMAGE_REPOSITORIES[@]}")" \
    || fail "docker pull of the pinned OCI registry image ${OCI_REGISTRY_IMAGE_DIGEST} failed from every mirror: ${OCI_REGISTRY_IMAGE_REPOSITORIES[*]}"
  # A digest pull has no tag; the Deployment matches on the image field with
  # imagePullPolicy Never, so re-tag to the exact name:tag it references.
  docker tag "${pulled}" "${image}" \
    || fail "docker tag of the pinned OCI registry image ${pulled} to ${image} failed"
  kind_load_image "${image}" "OCI registry"
}

# prepare_parity_alpine_image pulls the pinned alpine for the container
# plugin fixture. The digest reference stays in the local image store for
# drydock's offline `docker run --pull never`; the re-tagged name is what the
# repo-server sidecar runs with imagePullPolicy Never.
prepare_parity_alpine_image() {
  local image="drydock-argocd-parity-alpine:${CLUSTER_NAME}"
  PARITY_ALPINE_POLICY_IMAGE="$(pull_pinned_image "${PARITY_ALPINE_IMAGE_DIGEST}" "${PARITY_ALPINE_IMAGE_REPOSITORIES[@]}")" \
    || fail "docker pull of the pinned alpine image ${PARITY_ALPINE_IMAGE_DIGEST} failed from every mirror: ${PARITY_ALPINE_IMAGE_REPOSITORIES[*]}"
  docker tag "${PARITY_ALPINE_POLICY_IMAGE}" "${image}" \
    || fail "docker tag of the pinned alpine image ${PARITY_ALPINE_POLICY_IMAGE} to ${image} failed"
  kind_load_image "${image}" "plugin alpine"
}

# prepare_avp_image builds the argocd-vault-plugin sidecar image with no
# Dockerfile build, so nothing is fetched beyond the sha256-pinned release
# binary: the binary is copied into a container created from the
# already-pulled alpine, and the container is committed.
prepare_avp_image() {
  local image="drydock-argocd-parity-avp:${CLUSTER_NAME}"
  local arch expected actual binary url container version
  arch="$(daemon_linux_arch)"
  case "${arch}" in
    amd64) expected="${AVP_SHA256_LINUX_AMD64}" ;;
    arm64) expected="${AVP_SHA256_LINUX_ARM64}" ;;
    *) fail "no pinned argocd-vault-plugin sha256 for linux/${arch}" ;;
  esac
  [[ -n "${PARITY_ALPINE_POLICY_IMAGE}" ]] \
    || fail "PARITY_ALPINE_POLICY_IMAGE is unset; prepare_parity_alpine_image must run before prepare_avp_image"
  binary="${WORK_DIR}/argocd-vault-plugin"
  url="https://github.com/argoproj-labs/argocd-vault-plugin/releases/download/${AVP_VERSION}/argocd-vault-plugin_${AVP_VERSION#v}_linux_${arch}"
  retry "download of ${url}" curl -fsSL "${url}" -o "${binary}" \
    || fail "download of the argocd-vault-plugin ${AVP_VERSION} linux/${arch} release binary from ${url} failed"
  actual="$(openssl dgst -sha256 -r "${binary}" | cut -d ' ' -f 1)"
  [[ "${actual}" == "${expected}" ]] \
    || fail "argocd-vault-plugin ${AVP_VERSION} linux/${arch} sha256 mismatch: got ${actual}, want ${expected} (${url})"
  chmod 0755 "${binary}"
  container="$(docker create --pull never "${PARITY_ALPINE_POLICY_IMAGE}")" \
    || fail "docker create from the pinned alpine image ${PARITY_ALPINE_POLICY_IMAGE} failed"
  if ! docker cp "${binary}" "${container}:/usr/local/bin/argocd-vault-plugin" >/dev/null \
    || ! docker commit "${container}" "${image}" >/dev/null; then
    docker rm -f "${container}" >/dev/null 2>&1 || true
    fail "could not build ${image} from ${PARITY_ALPINE_POLICY_IMAGE} and the argocd-vault-plugin binary"
  fi
  docker rm -f "${container}" >/dev/null 2>&1 || true
  # Proves the binary runs in the image before the sidecar depends on it.
  version="$(docker run --rm --network none --pull never "${image}" argocd-vault-plugin version)" \
    || fail "argocd-vault-plugin version failed inside ${image}"
  [[ "${version}" == *" ${AVP_VERSION} "* ]] \
    || fail "argocd-vault-plugin in ${image} reports '${version}', want ${AVP_VERSION}"
  echo "argocd render parity smoke: ${version}" >&2
  kind_load_image "${image}" "argocd-vault-plugin"
}

install_fixture_registry() {
  kubectl -n argocd-parity create secret tls argocd-parity-registry-tls \
    --cert="${OCI_CA_FILE}" --key="${OCI_TLS_KEY_FILE}" >/dev/null \
    || fail "could not create the argocd-parity-registry-tls secret in namespace argocd-parity"
  kubectl -n argocd-parity apply -f - <<YAML >/dev/null || fail "could not apply the OCI registry Deployment and Service manifests"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-parity-registry
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: argocd-parity-registry
  template:
    metadata:
      labels:
        app.kubernetes.io/name: argocd-parity-registry
    spec:
      containers:
        - name: registry
          image: drydock-argocd-parity-registry:${CLUSTER_NAME}
          imagePullPolicy: Never
          env:
            - name: REGISTRY_HTTP_ADDR
              value: ":${OCI_REGISTRY_PORT}"
            - name: REGISTRY_HTTP_TLS_CERTIFICATE
              value: /certs/tls.crt
            - name: REGISTRY_HTTP_TLS_KEY
              value: /certs/tls.key
          ports:
            - containerPort: ${OCI_REGISTRY_PORT}
              name: registry
          volumeMounts:
            - name: registry-tls
              mountPath: /certs
              readOnly: true
      volumes:
        - name: registry-tls
          secret:
            secretName: argocd-parity-registry-tls
---
apiVersion: v1
kind: Service
metadata:
  name: argocd-parity-registry
spec:
  selector:
    app.kubernetes.io/name: argocd-parity-registry
  ports:
    - name: registry
      port: ${OCI_REGISTRY_PORT}
      targetPort: registry
YAML
  kubectl -n argocd-parity rollout status deployment/argocd-parity-registry --timeout=120s \
    || fail "OCI registry deployment argocd-parity-registry did not become ready within 120s"
}

start_registry_port_forward() {
  kubectl -n argocd-parity port-forward svc/argocd-parity-registry "${OCI_REGISTRY_PORT}:${OCI_REGISTRY_PORT}" >/dev/null 2>&1 &
  REGISTRY_PORT_FORWARD_PID="$!"
  # One probe validates the hosts entry, the forward, the TLS SAN, and
  # registry liveness together.
  for _ in {1..60}; do
    if curl -fsS --cacert "${OCI_CA_FILE}" "https://${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/v2/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "OCI registry was not reachable at https://${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/v2/ through the port-forward (check the /etc/hosts entry, the TLS SAN, and the registry pod)"
}

push_oci_artifact() {
  local ref stderr_file
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_ARTIFACT_REPOSITORY}:${OCI_ARTIFACT_TAG}"
  stderr_file="${OUT_DIR}/oras-push.stderr"
  # Push the content directory from inside it so the tar entries sit at the
  # extraction root (path: . in the fixture Application) as exactly one
  # tar+gzip content layer. helm push or per-file oras push would change the
  # media-type semantics.
  (cd "${OCI_ARTIFACT_PATH}" && oras push --ca-file "${OCI_CA_FILE}" "${ref}" . >/dev/null 2> "${stderr_file}") \
    || fail "oras push of the OCI parity artifact to ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
}

push_oci_chart() {
  local ref stderr_file
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_CHART_REPOSITORY}"
  stderr_file="${OUT_DIR}/helm-chart-push.stderr"
  # helm's own registry client, not oras: only it writes the helm config and
  # content media types Argo CD's repo-server and drydock both require, and
  # the smoke must not depend on a helm binary being installed.
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-chart-push \
    --chart-dir "${OCI_CHART_PATH}" \
    --ref "${ref}" \
    --ca-file "${OCI_CA_FILE}" \
    > /dev/null 2> "${stderr_file}") \
    || fail "helm chart push of the nested OCI parity chart to ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
}

verify_oci_chart_manifest() {
  local ref manifest stderr_file config_count layer_count media_type_count
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_CHART_REPOSITORY}/parity-nested-chart:1.0.0"
  stderr_file="${OUT_DIR}/oras-chart-manifest-fetch.stderr"
  manifest="$(oras manifest fetch --ca-file "${OCI_CA_FILE}" "${ref}" 2> "${stderr_file}")" \
    || fail "oras manifest fetch for the pushed nested OCI parity chart ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
  config_count="$(grep -o 'application/vnd\.cncf\.helm\.config\.v1+json' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  layer_count="$(grep -o 'application/vnd\.cncf\.helm\.chart\.content\.v1\.tar+gzip' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  media_type_count="$(grep -o '"mediaType"' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  # Expect exactly two mediaType entries — the helm config and one helm chart
  # content layer, with no provenance layer and no extra content layer. Helm's
  # registry client writes no top-level manifest mediaType, unlike the oras
  # push in verify_oci_artifact_manifest, so the total is two and not three.
  if [[ "${config_count}" != "1" || "${layer_count}" != "1" || "${media_type_count}" != "2" ]]; then
    printf '%s\n' "${manifest}" > "${OUT_DIR}/oci-chart-manifest.json"
    fail "pushed nested OCI parity chart ${ref} has the wrong manifest shape: want one application/vnd.cncf.helm.config.v1+json config and exactly one application/vnd.cncf.helm.chart.content.v1.tar+gzip layer, got ${config_count} helm configs and ${layer_count} helm content layers across ${media_type_count} mediaType entries; see ${OUT_DIR}/oci-chart-manifest.json"
  fi
}

verify_oci_artifact_manifest() {
  local ref manifest stderr_file layer_count media_type_count
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_ARTIFACT_REPOSITORY}:${OCI_ARTIFACT_TAG}"
  stderr_file="${OUT_DIR}/oras-manifest-fetch.stderr"
  manifest="$(oras manifest fetch --ca-file "${OCI_CA_FILE}" "${ref}" 2> "${stderr_file}")" \
    || fail "oras manifest fetch for the pushed OCI parity artifact ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
  layer_count="$(grep -o 'application/vnd\.oci\.image\.layer\.v1\.tar+gzip' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  media_type_count="$(grep -o '"mediaType"' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  # Expect exactly three mediaType entries (manifest, empty config, one
  # layer) with the single layer being the tar+gzip content type.
  if [[ "${layer_count}" != "1" || "${media_type_count}" != "3" ]]; then
    printf '%s\n' "${manifest}" > "${OUT_DIR}/oci-artifact-manifest.json"
    fail "pushed OCI parity artifact ${ref} has the wrong manifest shape: want exactly one application/vnd.oci.image.layer.v1.tar+gzip content layer, got ${layer_count} tar+gzip layers across ${media_type_count} mediaType entries; see ${OUT_DIR}/oci-artifact-manifest.json"
  fi
}

wait_for_oci_application() {
  local app sync_status
  # Both registry-backed Applications: the artifact source and the nested OCI
  # Helm chart source. Naming the app in the failure tells a TLS problem
  # (argocd-tls-certs-cm) apart from a media-type or chart-name problem.
  for app in "${OCI_APPLICATION}" "${OCI_CHART_APPLICATION}"; do
    sync_status=""
    for _ in {1..120}; do
      sync_status="$(kubectl -n argocd get application "${app}" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
      if [[ "${sync_status}" == "Synced" || "${sync_status}" == "OutOfSync" ]]; then
        break
      fi
      sleep 2
    done
    if [[ "${sync_status}" != "Synced" && "${sync_status}" != "OutOfSync" ]]; then
      kubectl -n argocd get application "${app}" -o yaml > "${OUT_DIR}/${app}.yaml" 2>&1 || true
      fail "OCI Application ${app} did not reach a comparable sync state (Argo CD could not fetch it from ${OCI_REGISTRY_HOST}; check the argocd-tls-certs-cm CA, the registry service, and the pushed manifest media types); see ${OUT_DIR}/${app}.yaml"
    fi
  done
}

warm_drydock_oci_cache() {
  local app stderr_file
  # The artifact app fills --oci-cache-dir, the chart app fills
  # --chart-cache-dir; the offline capture loop reads both back.
  for app in "${OCI_APPLICATION}" "${OCI_CHART_APPLICATION}"; do
    stderr_file="${OUT_DIR}/drydock-oci-warm-${app}.stderr"
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
      --path "${FIXTURE_REPO_PATH}" \
      --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
      --oci-cache-dir "${OCI_CACHE_DIR}" \
      --chart-cache-dir "${CHART_CACHE_DIR}" \
      --oci-ca-file "${OCI_CA_FILE}" \
      --render-cache-dir "${OCI_WARM_RENDER_CACHE_DIR}" \
      > /dev/null 2> "${stderr_file}") \
      || fail "drydock OCI cache warm (non-offline build of ${app} with --oci-cache-dir/--chart-cache-dir/--oci-ca-file) failed; see ${stderr_file}"
    rm -f "${stderr_file}"
  done
}

install_argocd() {
  local version="$1"
  kubectl create namespace argocd >/dev/null
  kubectl -n argocd apply --server-side --force-conflicts -f "https://raw.githubusercontent.com/argoproj/argo-cd/${version}/manifests/install.yaml" >/dev/null
  kubectl wait --for=condition=Established crd/applications.argoproj.io --timeout=120s
  kubectl wait --for=condition=Established crd/applicationsets.argoproj.io --timeout=120s
  kubectl -n argocd patch configmap argocd-cm --type merge -p '{"data":{"kustomize.buildOptions":"--enable-helm"}}' >/dev/null
  # Argo CD derives the OCI registry CA from argocd-tls-certs-cm keyed by the
  # bare hostname without port. The patch must land before the rollout
  # restart below so repo-server pods mount the CA from the start (ConfigMap
  # volume propagation to a running pod races the kubelet sync).
  [[ -s "${OCI_CA_FILE}" ]] \
    || fail "OCI registry CA certificate not found at ${OCI_CA_FILE} before Argo CD install; certificate generation must run first"
  kubectl -n argocd create configmap argocd-tls-certs-cm \
    --from-file="${OCI_REGISTRY_HOST}=${OCI_CA_FILE}" \
    --dry-run=client -o json > "${WORK_DIR}/argocd-tls-certs-cm.patch.json" \
    || fail "could not build the argocd-tls-certs-cm patch payload from ${OCI_CA_FILE}"
  kubectl -n argocd patch configmap argocd-tls-certs-cm --type merge \
    --patch-file "${WORK_DIR}/argocd-tls-certs-cm.patch.json" >/dev/null \
    || fail "could not patch argocd-tls-certs-cm with the ${OCI_REGISTRY_HOST} CA bundle"
  kubectl -n argocd rollout restart deployment/argocd-repo-server >/dev/null
  kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s
  kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
  kubectl -n argocd rollout status deployment/argocd-applicationset-controller --timeout=300s
  kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=300s
}

# wait_for_cmp_socket gates on a sidecar's plugin socket. rollout status only
# proves the container is Running; this makes a bad plugin descriptor fail
# here with a clear message instead of minutes later as "did not generate
# manifests".
wait_for_cmp_socket() {
  local sidecar="$1"
  local socket="/home/argocd/cmp-server/plugins/${sidecar}.sock"
  local sidecar_log
  for _ in {1..60}; do
    if kubectl -n argocd exec deployment/argocd-repo-server -c "${sidecar}" -- \
      sh -c "test -S ${socket}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  # Same filename collect_logs writes from the EXIT trap, so the later, longer
  # tail supersedes this dump instead of leaving two copies.
  sidecar_log="$(artifact_dir logs)/argocd-repo-server-${sidecar}.log"
  kubectl -n argocd logs deployment/argocd-repo-server -c "${sidecar}" --tail=200 > "${sidecar_log}" 2>&1 || true
  fail "argocd-cmp-server in sidecar ${sidecar} never bound ${socket}; see ${sidecar_log}"
}

# install_avp_backend applies the Secret the AVP sidecar's kubernetessecret
# backend reads (drydock's public redaction markers, no secret value) and the
# RBAC that lets the repo-server ServiceAccount get it, then confirms the
# grant, so a broken binding fails here and not as a generate error.
install_avp_backend() {
  kubectl apply -f "${SIDECAR_PATH}/avp-backend.yaml" -f "${SIDECAR_PATH}/avp-rbac.yaml" >/dev/null \
    || fail "could not apply the argocd-vault-plugin backend Secret and RBAC from ${SIDECAR_PATH}"
  for _ in {1..30}; do
    if kubectl auth can-i get secrets/parity-avp-backend -n argocd \
      --as=system:serviceaccount:argocd:argocd-repo-server >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "ServiceAccount argocd/argocd-repo-server still cannot get secret parity-avp-backend after applying ${SIDECAR_PATH}/avp-rbac.yaml"
}

install_cmp_sidecar() {
  local version="$1"
  local patch_file sidecar
  patch_file="${WORK_DIR}/repo-server-cmp-patch.yaml"
  # The ConfigMaps must exist before the patch: each sidecar mounts its own as
  # a non-optional configMap volume, so a patch that landed first would leave
  # the new ReplicaSet in ContainerCreating and the rollout below would burn
  # its whole timeout on a confusing message.
  kubectl -n argocd create configmap parity-env-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin.yaml" >/dev/null \
    || fail "could not create the parity-env-cmp ConfigMap from ${SIDECAR_PATH}/plugin.yaml"
  kubectl -n argocd create configmap parity-container-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin-container.yaml" >/dev/null \
    || fail "could not create the parity-container-cmp ConfigMap from ${SIDECAR_PATH}/plugin-container.yaml"
  kubectl -n argocd create configmap parity-avp-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin-avp.yaml" >/dev/null \
    || fail "could not create the parity-avp-cmp ConfigMap from ${SIDECAR_PATH}/plugin-avp.yaml"
  install_avp_backend
  sed -e "s|__ARGOCD_IMAGE__|quay.io/argoproj/argocd:${version}|" \
    -e "s|__PARITY_ALPINE_IMAGE__|drydock-argocd-parity-alpine:${CLUSTER_NAME}|" \
    -e "s|__PARITY_AVP_IMAGE__|drydock-argocd-parity-avp:${CLUSTER_NAME}|" \
    "${SIDECAR_PATH}/repo-server-patch.yaml" > "${patch_file}" \
    || fail "could not render the repo-server CMP sidecar patch from ${SIDECAR_PATH}/repo-server-patch.yaml"
  if grep -q '__[A-Z_]*__' "${patch_file}"; then
    fail "the rendered repo-server CMP sidecar patch ${patch_file} still has an unsubstituted __PLACEHOLDER__"
  fi
  # install.yaml is applied server-side once and never re-applied, so a
  # client-side strategic patch afterwards is safe, and the patch itself
  # rolls the Deployment - no restart call needed.
  kubectl -n argocd patch deployment argocd-repo-server --type strategic \
    --patch-file "${patch_file}" >/dev/null \
    || fail "could not patch argocd-repo-server with the parity CMP sidecars"
  kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s
  for sidecar in "${CMP_SIDECARS[@]}"; do
    wait_for_cmp_socket "${sidecar}"
  done
}

login_argocd() {
  local password
  password="$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::add-mask::${password}"
  fi
  kubectl -n argocd port-forward svc/argocd-server 18080:443 >/dev/null 2>&1 &
  PORT_FORWARD_PID="$!"
  for _ in {1..60}; do
    if curl -kfsS https://localhost:18080 >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  argocd login localhost:18080 --username admin --password "${password}" --insecure >/dev/null
}

apply_fixture_apps() {
  ensure_namespace "${TENANT_NAMESPACE}"
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/projects" >/dev/null
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applications" >/dev/null
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applicationsets" >/dev/null
  kubectl -n "${TENANT_NAMESPACE}" apply -f "${FIXTURE_REPO_PATH}/tenant-applications" >/dev/null
}

wait_for_application() {
  local namespace="$1"
  local app="$2"
  local reconciled_at
  for _ in {1..120}; do
    if kubectl -n "${namespace}" get application "${app}" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  kubectl -n "${namespace}" get application "${app}" >/dev/null
  # `argocd app manifests` serves the controller's cached comparison, so an
  # Application the controller never reconciled returns exit 0 with empty
  # output. Gate on reconciledAt so that failure surfaces here instead of as a
  # misleading "did not generate manifests" after the capture retry loop.
  for _ in {1..120}; do
    reconciled_at="$(kubectl -n "${namespace}" get application "${app}" -o jsonpath='{.status.reconciledAt}' 2>/dev/null || true)"
    if [[ -n "${reconciled_at}" ]]; then
      return 0
    fi
    sleep 2
  done
  local dump
  dump="$(artifact_dir logs)/unreconciled-${namespace}-${app}.yaml"
  kubectl -n "${namespace}" get application "${app}" -o yaml > "${dump}" 2>&1 || true
  fail "Argo CD did not reconcile Application ${namespace}/${app}; see ${dump}"
}

wait_for_applications() {
  local app
  for app in "${APPLICATIONS[@]}"; do
    wait_for_application argocd "${app}"
  done
  for app in "${TENANT_APPLICATIONS[@]}"; do
    wait_for_application "${TENANT_NAMESPACE}" "${app}"
  done
}

assert_namespace_inventory() {
  local namespace="$1"
  local diff_file="$2"
  shift 2
  local expected actual
  expected="$(printf '%s\n' "$@" | sort)"
  actual="$(kubectl -n "${namespace}" get applications.argoproj.io -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)"
  if [[ "${actual}" != "${expected}" ]]; then
    {
      echo "expected Applications:"
      printf '%s\n' "${expected}"
      echo
      echo "actual Applications:"
      printf '%s\n' "${actual}"
    } > "${OUT_DIR}/${diff_file}"
    fail "Argo CD Application inventory in ${namespace} did not match expected list; see ${OUT_DIR}/${diff_file}"
  fi
}

assert_application_inventory() {
  assert_namespace_inventory argocd application-inventory.diff "${APPLICATIONS[@]}"
  assert_namespace_inventory "${TENANT_NAMESPACE}" tenant-application-inventory.diff "${TENANT_APPLICATIONS[@]}"
}

capture_argocd_manifest() {
  local app_ref="$1"
  local stem="$2"
  local output_dir="$3"
  for _ in {1..120}; do
    if argocd app manifests "${app_ref}" > "${output_dir}/${stem}.yaml" 2> "${OUT_DIR}/argocd-${stem}.stderr"; then
      rm -f "${OUT_DIR}/argocd-${stem}.stderr"
      break
    fi
    sleep 2
  done
  if [[ ! -s "${output_dir}/${stem}.yaml" ]]; then
    fail "Argo CD did not generate manifests for ${app_ref}; see ${OUT_DIR}/argocd-${stem}.stderr"
  fi
}

capture_argocd_manifests() {
  local app output_dir masked_dir
  output_dir="$(artifact_dir argocd-manifests)"
  # The Secrets-only AVP Application renders masked (++++++++) here, so it
  # stays out of the exact comparison and is recorded only as proof that the
  # sidecar generated it under Argo CD; the oracle compares it unmasked.
  masked_dir="$(artifact_dir avp-oracle/argocd-masked-manifests)"
  for app in "${APPLICATIONS[@]}"; do
    if [[ "${app}" == "${AVP_SECRET_APPLICATION}" ]]; then
      capture_argocd_manifest "${app}" "${app}" "${masked_dir}"
      continue
    fi
    capture_argocd_manifest "${app}" "${app}" "${output_dir}"
  done
  for app in "${TENANT_APPLICATIONS[@]}"; do
    capture_argocd_manifest "${TENANT_NAMESPACE}/${app}" "${app}" "${output_dir}"
  done
}

capture_drydock_manifest() {
  local app_ref="$1"
  local stem="$2"
  local output_dir="$3"
  local plugin_app stderr_file
  local build_args=(build app "${app_ref}"
    --path "${FIXTURE_REPO_PATH}"
    --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}"
    --oci-cache-dir "${OCI_CACHE_DIR}"
    --chart-cache-dir "${CHART_CACHE_DIR}"
    --oci-ca-file "${OCI_CA_FILE}"
    --render-cache-dir "${OCI_OFFLINE_RENDER_CACHE_DIR}"
    --offline)
  for plugin_app in "${PLUGIN_APPLICATIONS[@]}"; do
    if [[ "${stem}" == "${plugin_app}" ]]; then
      # Scoped to the plugin app on purpose: --plugin-policy-ref makes a
      # missing policy fatal for every app it is passed to, and each
      # invocation materializes the whole policy-repo tree into a temp dir.
      # exec engines also require a non-empty --plugin-policy-ref for trust,
      # so these flags are exactly what makes this render work at all.
      [[ -n "${FIXTURE_GIT_WORK}" ]] \
        || fail "FIXTURE_GIT_WORK is unset; prepare_fixture_git_image must run before capturing ${stem}"
      build_args+=(--enable-plugins
        --plugin-policy-repo "${FIXTURE_GIT_WORK}"
        --plugin-policy-ref HEAD)
      break
    fi
  done
  stderr_file="${OUT_DIR}/drydock-${stem}.stderr"
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" "${build_args[@]}" \
    > "${output_dir}/${stem}.yaml" 2> "${stderr_file}"); then
    # The stderr file sits outside the uploaded artifact directories, so
    # print it into the job log where it is the only diagnosis.
    echo "argocd render parity smoke: drydock capture of ${app_ref} failed; its stderr follows" >&2
    cat "${stderr_file}" >&2 || true
    fail "drydock capture of ${app_ref} failed; see ${stderr_file}"
  fi
  rm -f "${stderr_file}"
}

# preflight_container_plugin_capture checks what drydock's container engine
# needs before the capture that uses it, so a host gap fails with a named
# cause rather than a generic plugin error. It mirrors drydock's own lookups:
# docker only on the controlled PATH, and (offline) an isolated empty Docker
# client config, so the default context's endpoint - DOCKER_HOST or
# /var/run/docker.sock - must hold the image, not the shell's current context.
preflight_container_plugin_capture() {
  local dir docker_path="" client_config
  for dir in /usr/local/bin /usr/bin /bin; do
    if [[ -f "${dir}/docker" && -x "${dir}/docker" ]]; then
      docker_path="${dir}/docker"
      break
    fi
  done
  [[ -n "${docker_path}" ]] \
    || fail "drydock's container engine looks up docker only on /usr/local/bin:/usr/bin:/bin and none of them has it (docker on PATH: $(command -v docker || echo none)); ${CONTAINER_PLUGIN_APPLICATION} cannot render"
  client_config="${WORK_DIR}/docker-preflight-config"
  mkdir -p "${client_config}"
  DOCKER_CONFIG="${client_config}" "${docker_path}" image inspect "${PARITY_ALPINE_POLICY_IMAGE}" >/dev/null 2>&1 \
    || fail "${docker_path} with an empty client config (as drydock runs it offline) cannot see ${PARITY_ALPINE_POLICY_IMAGE}; drydock runs it with --pull never, so ${CONTAINER_PLUGIN_APPLICATION} cannot render. Locally: unset DOCKER_CONTEXT, and if /var/run/docker.sock is absent set DOCKER_HOST to the endpoint \`docker context inspect\` reports"
}

capture_drydock_manifests() {
  local app output_dir
  output_dir="$(artifact_dir drydock-manifests)"
  preflight_container_plugin_capture
  for app in "${APPLICATIONS[@]}"; do
    # Rendered by capture_avp_oracle_manifests into the oracle directory, so
    # the masked comparison sees the same Application set on both sides.
    [[ "${app}" == "${AVP_SECRET_APPLICATION}" ]] && continue
    capture_drydock_manifest "argocd/${app}" "${app}" "${output_dir}"
  done
  for app in "${TENANT_APPLICATIONS[@]}"; do
    capture_drydock_manifest "${TENANT_NAMESPACE}/${app}" "${app}" "${output_dir}"
  done
}

# capture_avp_oracle_manifests is the second AVP oracle. `argocd app
# manifests` masks Secret data as ++++++++, so it cannot see a base64
# Secret.data substitution. Each oracle Application's source directory is
# copied into the argocd-vault-plugin sidecar's private /tmp and rendered
# there with `argocd-vault-plugin generate`: the same binary, AVP_TYPE,
# backend Secret and projected ServiceAccount token the Argo CD side used.
# That unmasked output and drydock's render of the same Application land in
# avp-oracle/ for compare_avp_oracle_manifests.
capture_avp_oracle_manifests() {
  local pod app source_path remote avp_dir drydock_dir stderr_file
  avp_dir="$(artifact_dir avp-oracle/argocd-vault-plugin-manifests)"
  drydock_dir="$(artifact_dir avp-oracle/drydock-manifests)"
  # One pod for both execs: a copy and a generate that landed on different
  # repo-server pods would render an empty directory.
  pod="$(kubectl -n argocd get pods -l app.kubernetes.io/name=argocd-repo-server \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')" \
    || fail "could not list argocd-repo-server pods for the argocd-vault-plugin oracle"
  [[ -n "${pod}" ]] || fail "no running argocd-repo-server pod for the argocd-vault-plugin oracle"
  for app in "${AVP_ORACLE_APPLICATIONS[@]}"; do
    source_path="$(kubectl -n argocd get application "${app}" -o jsonpath='{.spec.source.path}')" \
      || fail "could not read spec.source.path of Application argocd/${app}"
    [[ -n "${source_path}" ]] || fail "Application argocd/${app} has no spec.source.path for the argocd-vault-plugin oracle"
    [[ -d "${FIXTURE_REPO_PATH}/${source_path}" ]] \
      || fail "Application argocd/${app} source path ${source_path} is not a directory under ${FIXTURE_REPO_PATH}"
    remote="/tmp/drydock-avp-oracle/${app}"
    # COPYFILE_DISABLE keeps macOS tar from adding ._* AppleDouble entries,
    # which AVP would read as YAML files.
    COPYFILE_DISABLE=1 tar -C "${FIXTURE_REPO_PATH}/${source_path}" -cf - . \
      | kubectl -n argocd exec -i "${pod}" -c argocd-vault-plugin -- \
        sh -c "rm -rf '${remote}' && mkdir -p '${remote}' && tar -xf - -C '${remote}'" \
      || fail "could not copy ${source_path} into the argocd-vault-plugin sidecar of ${pod} at ${remote}"
    stderr_file="${OUT_DIR}/avp-oracle/argocd-vault-plugin-${app}.stderr"
    if ! kubectl -n argocd exec "${pod}" -c argocd-vault-plugin -- \
      argocd-vault-plugin generate "${remote}" > "${avp_dir}/${app}.yaml" 2> "${stderr_file}"; then
      echo "argocd render parity smoke: argocd-vault-plugin generate of ${source_path} failed in the sidecar; its stderr follows" >&2
      cat "${stderr_file}" >&2 || true
      fail "argocd-vault-plugin generate of ${source_path} failed in the sidecar; see ${stderr_file}"
    fi
    rm -f "${stderr_file}"
    [[ -s "${avp_dir}/${app}.yaml" ]] \
      || fail "argocd-vault-plugin generate of ${source_path} in the sidecar produced no output"
    capture_drydock_manifest "argocd/${app}" "${app}" "${drydock_dir}"
  done
}

# count_avp_markers prints how many drydock-redacted- markers a manifest file
# carries: literally, plus inside every standard base64 token it contains. A
# substituted Secret.data value is base64 on both sides, so a literal count
# alone would let "neither side decoded anything" pass there.
count_avp_markers() {
  local file="$1" token
  {
    grep -o 'drydock-redacted-[0-9a-f]\{12\}' "${file}" || true
    while IFS= read -r token; do
      printf '%s' "${token}" | base64 -d 2>/dev/null \
        | grep -ao 'drydock-redacted-[0-9a-f]\{12\}' || true
    done < <(grep -oE '[A-Za-z0-9+/]{16,}={0,2}' "${file}" || true)
  } | wc -l | tr -d ' '
}

# check_avp_marker_count reports one file's marker count against the expected
# one and returns 1 on a mismatch, so assert_avp_markers can print every side
# before failing.
check_avp_marker_count() {
  local file="$1" want="$2" label="$3" count
  count="$(count_avp_markers "${file}")"
  echo "argocd render parity smoke: ${label} markers: ${count} (want ${want})" >&2
  [[ "${count}" == "${want}" ]]
}

# assert_avp_markers keeps "both sides left the placeholders alone" from
# passing the exact comparisons: the masked sides must render exactly
# AVP_EXPECTED_MARKERS markers for the AVP fixture, and the oracle sides
# AVP_EXPECTED_MARKERS and AVP_SECRET_EXPECTED_MARKERS for their two
# Applications.
assert_avp_markers() {
  local side mismatch="false"
  log_step "Counting redaction markers on every AVP side"
  for side in argocd drydock; do
    check_avp_marker_count "${OUT_DIR}/${side}-manifests/${AVP_APPLICATION}.yaml" \
      "${AVP_EXPECTED_MARKERS}" "${AVP_APPLICATION} ${side}" || mismatch="true"
  done
  for side in argocd-vault-plugin drydock; do
    check_avp_marker_count "${OUT_DIR}/avp-oracle/${side}-manifests/${AVP_APPLICATION}.yaml" \
      "${AVP_EXPECTED_MARKERS}" "${AVP_APPLICATION} oracle ${side}" || mismatch="true"
    check_avp_marker_count "${OUT_DIR}/avp-oracle/${side}-manifests/${AVP_SECRET_APPLICATION}.yaml" \
      "${AVP_SECRET_EXPECTED_MARKERS}" "${AVP_SECRET_APPLICATION} oracle ${side}" || mismatch="true"
  done
  [[ "${mismatch}" == "false" ]] \
    || fail "every AVP side must render exactly its expected drydock-redacted- marker count; see ${OUT_DIR}/argocd-manifests/${AVP_APPLICATION}.yaml, ${OUT_DIR}/drydock-manifests/${AVP_APPLICATION}.yaml and ${OUT_DIR}/avp-oracle/"
}

# compare_avp_oracle_manifests runs the exact per-resource comparison on the
# oracle output, with no Secret masking: the "argocd" side of the comparer's
# output is the sidecar's `argocd-vault-plugin generate` output here.
compare_avp_oracle_manifests() {
  log_step "Comparing argocd-vault-plugin sidecar output and drydock rendered manifests"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${OUT_DIR}/avp-oracle/argocd-vault-plugin-manifests" \
    --drydock-dir "${OUT_DIR}/avp-oracle/drydock-manifests" \
    --out-dir "${OUT_DIR}/compare-avp-oracle" \
    --ignore-file "${IGNORE_FILE}")
}

compare_manifests() {
  log_step "Comparing Argo CD and drydock rendered manifests"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${OUT_DIR}/argocd-manifests" \
    --drydock-dir "${OUT_DIR}/drydock-manifests" \
    --out-dir "${OUT_DIR}/compare" \
    --ignore-file "${IGNORE_FILE}")
}

compare_tracking_manifests() {
  local app argocd_dir drydock_dir
  log_step "Comparing tracking metadata without ignore rules"
  argocd_dir="$(artifact_dir argocd-tracking-manifests)"
  drydock_dir="$(artifact_dir drydock-tracking-manifests)"
  for app in "${TRACKING_APPLICATIONS[@]}"; do
    cp "${OUT_DIR}/argocd-manifests/${app}.yaml" "${argocd_dir}/${app}.yaml"
    cp "${OUT_DIR}/drydock-manifests/${app}.yaml" "${drydock_dir}/${app}.yaml"
  done
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${argocd_dir}" \
    --drydock-dir "${drydock_dir}" \
    --out-dir "${OUT_DIR}/compare-tracking")
}

ensure_namespace() {
  local namespace="$1"
  kubectl get namespace "${namespace}" >/dev/null 2>&1 || kubectl create namespace "${namespace}" >/dev/null
}

configure_application_namespaces() {
  kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge \
    -p "{\"data\":{\"application.namespaces\":\"${TENANT_NAMESPACE},project-policy-tenant\"}}" >/dev/null
  kubectl -n argocd rollout restart deployment/argocd-server >/dev/null
  kubectl -n argocd rollout restart statefulset/argocd-application-controller >/dev/null
  kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
  kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=300s
}

prepare_project_policy_namespaces() {
  ensure_namespace project-policy-tenant
  ensure_namespace project-policy-workloads
}

apply_project_policy_apps() {
  kubectl apply -f "${PROJECT_POLICY_REPO_PATH}/project-policy/projects.yaml" >/dev/null
  kubectl apply -f "${PROJECT_POLICY_REPO_PATH}/project-policy/applications.yaml" >/dev/null
}

project_policy_condition_categories() {
  local namespace="$1"
  local app="$2"
  local conditions condition_type message normalized category
  conditions="$(kubectl -n "${namespace}" get application "${app}" -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.message}{"\n"}{end}' 2>/dev/null || true)"
  while IFS=$'\t' read -r condition_type message; do
    [[ -n "${message}" ]] || continue
    normalized="$(printf '%s' "${message}" | tr '[:upper:]' '[:lower:]')"
    category="unknown"
    if [[ "${condition_type}" == "UnknownError" && "${normalized}" == *" in namespace "* && "${normalized}" == *" is not permitted to use project "* ]]; then
      category="source-namespace"
    elif [[ "${condition_type}" == "InvalidSpecError" ]]; then
      if [[ "${normalized}" == *"source namespace"* ]]; then
        category="source-namespace"
      elif [[ "${normalized}" == *destination* ]]; then
        category="destination"
      elif [[ "${normalized}" == *source* || "${normalized}" == *repo* ]]; then
        category="source"
      fi
    else
      continue
    fi
    printf '%s\n' "${category}"
  done <<< "${conditions}"
}

project_policy_has_condition_category() {
  local namespace="$1"
  local app="$2"
  local expected="$3"
  local category
  while IFS= read -r category; do
    [[ "${category}" == "${expected}" ]] && return 0
  done < <(project_policy_condition_categories "${namespace}" "${app}")
  return 1
}

project_policy_has_condition() {
  local namespace="$1"
  local app="$2"
  [[ -n "$(project_policy_condition_categories "${namespace}" "${app}")" ]]
}

wait_for_project_policy_application() {
  local namespace="$1"
  local app="$2"
  local expected_category="$3"
  local reconciled_at
  for _ in {1..180}; do
    if ! kubectl -n "${namespace}" get application "${app}" >/dev/null 2>&1; then
      sleep 2
      continue
    fi
    if [[ "${expected_category}" == "none" ]]; then
      reconciled_at="$(kubectl -n "${namespace}" get application "${app}" -o jsonpath='{.status.reconciledAt}' 2>/dev/null || true)"
      if [[ -n "${reconciled_at}" ]] && ! project_policy_has_condition "${namespace}" "${app}"; then
        return 0
      fi
    elif project_policy_has_condition_category "${namespace}" "${app}" "${expected_category}"; then
      return 0
    fi
    sleep 2
  done
  local failure_dir
  failure_dir="$(artifact_dir project-policy)"
  kubectl -n "${namespace}" get application "${app}" -o yaml > "${failure_dir}/${app}.yaml" 2>&1 || true
  fail "project-policy Application ${namespace}/${app} did not reach expected policy condition category ${expected_category}; see ${failure_dir}/${app}.yaml"
}

wait_for_project_policy_applications() {
  local case_entry namespace app expected_category
  for case_entry in "${PROJECT_POLICY_CASES[@]}"; do
    IFS='|' read -r namespace app expected_category <<< "${case_entry}"
    wait_for_project_policy_application "${namespace}" "${app}" "${expected_category}"
  done
}

capture_project_policy_argocd_applications() {
  local case_entry namespace app expected_category output_dir
  output_dir="$(artifact_dir project-policy/argocd-applications)"
  for case_entry in "${PROJECT_POLICY_CASES[@]}"; do
    IFS='|' read -r namespace app expected_category <<< "${case_entry}"
    kubectl -n "${namespace}" get application "${app}" -o json > "${output_dir}/${app}.json"
  done
}

capture_project_policy_drydock_diagnostics() {
  local output_dir stderr_file
  output_dir="$(artifact_dir project-policy)"
  stderr_file="${output_dir}/drydock-diagnostics.stderr"
  (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" diag \
    --path "${PROJECT_POLICY_REPO_PATH}" \
    --repo-map "${FIXTURE_REPO_URL}=${PROJECT_POLICY_REPO_PATH}" \
    --offline \
    --render \
    --project-diagnostics all \
    -o json > "${output_dir}/drydock-diagnostics.json" 2> "${stderr_file}")
  rm -f "${stderr_file}"
}

compare_project_policy_smoke() {
  local output_dir stderr_file
  output_dir="$(artifact_dir project-policy)"
  stderr_file="${output_dir}/summary.stderr"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-project-policy-smoke \
    --argocd-app-dir "${output_dir}/argocd-applications" \
    --drydock-diagnostics "${output_dir}/drydock-diagnostics.json" \
    --expected "${PROJECT_POLICY_EXPECTED}" \
    --out "${output_dir}/summary.txt" 2> "${stderr_file}")
  rm -f "${stderr_file}"
}

run_project_policy_smoke() {
  log_step "Preparing project-policy namespaces"
  prepare_project_policy_namespaces
  log_step "Applying project-policy AppProjects and Applications"
  apply_project_policy_apps
  log_step "Waiting for project-policy Application policy outcomes"
  wait_for_project_policy_applications
  log_step "Capturing project-policy Argo CD Application status"
  capture_project_policy_argocd_applications
  log_step "Capturing project-policy drydock diagnostics"
  capture_project_policy_drydock_diagnostics
  log_step "Comparing project-policy outcomes"
  compare_project_policy_smoke
}

main() {
  local argocd_version
  argocd_version="$(resolve_argocd_version)"
  echo "Argo CD render parity smoke: ${argocd_version}" >&2
  log_step "Ensuring /etc/hosts entry for the OCI registry"
  ensure_registry_hosts_entry
  log_step "Generating OCI registry TLS certificate"
  generate_registry_certificate
  log_step "Installing Argo CD CLI ${argocd_version}"
  install_argocd_cli "${argocd_version}"
  if [[ "${CREATE_CLUSTER}" == "true" ]]; then
    log_step "Creating kind cluster ${CLUSTER_NAME}"
    kind create cluster --name "${CLUSTER_NAME}"
  else
    log_step "Using existing kind cluster ${CLUSTER_NAME}"
    kind export kubeconfig --name "${CLUSTER_NAME}"
  fi
  # Before the Git server: a fallback-mirror pull rewrites the container
  # plugin policy in the git snapshot that step commits.
  log_step "Preparing the pinned container plugin image"
  prepare_parity_alpine_image
  log_step "Preparing the argocd-vault-plugin sidecar image"
  prepare_avp_image
  log_step "Preparing fixture Git server"
  prepare_fixture_git_image
  install_fixture_git_server
  log_step "Preparing fixture OCI registry"
  prepare_registry_image
  install_fixture_registry
  log_step "Starting OCI registry port-forward"
  start_registry_port_forward
  log_step "Pushing OCI parity artifact"
  push_oci_artifact
  verify_oci_artifact_manifest
  log_step "Pushing nested OCI parity Helm chart"
  push_oci_chart
  verify_oci_chart_manifest
  log_step "Installing Argo CD ${argocd_version}"
  install_argocd "${argocd_version}"
  log_step "Installing the parity CMP sidecar"
  install_cmp_sidecar "${argocd_version}"
  # Restarting argocd-server must happen before login_argocd starts the
  # port-forward it binds to a single server pod.
  log_step "Enabling Applications in tenant namespaces"
  configure_application_namespaces
  log_step "Logging in to Argo CD"
  login_argocd
  log_step "Applying parity fixture Applications"
  apply_fixture_apps
  log_step "Waiting for expected Applications"
  wait_for_applications
  assert_application_inventory
  log_step "Waiting for the OCI Application comparison"
  wait_for_oci_application
  log_step "Capturing Argo CD rendered manifests"
  capture_argocd_manifests
  log_step "Warming drydock OCI artifact and chart caches"
  warm_drydock_oci_cache
  log_step "Capturing drydock rendered manifests"
  capture_drydock_manifests
  log_step "Rendering the argocd-vault-plugin oracle inside the sidecar"
  capture_avp_oracle_manifests
  assert_avp_markers
  compare_manifests
  compare_avp_oracle_manifests
  compare_tracking_manifests
  if [[ "${RUN_PROJECT_POLICY_SMOKE}" == "true" ]]; then
    run_project_policy_smoke
  else
    log_step "Skipping project-policy smoke"
  fi
  echo "Argo CD render parity smoke complete: ${OUT_DIR}" >&2
}

main
