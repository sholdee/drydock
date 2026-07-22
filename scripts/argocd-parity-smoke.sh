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
PROJECT_POLICY_REPO_PATH="${REPO_ROOT}/testdata/argocd-project-policy/repo"
PROJECT_POLICY_EXPECTED="${REPO_ROOT}/testdata/argocd-project-policy/expected.yaml"
OCI_ARTIFACT_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-artifact"
OCI_REGISTRY_HOST="argocd-parity-registry.argocd-parity.svc.cluster.local"
OCI_REGISTRY_PORT="5443"
# Multi-arch index digest for registry:2.8.3 on the Docker-official ECR
# mirror (covers CI amd64 and local darwin arm64; no Docker Hub 429 surface).
OCI_REGISTRY_IMAGE="public.ecr.aws/docker/library/registry@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
OCI_ARTIFACT_REPOSITORY="parity/config"
OCI_ARTIFACT_TAG="v1.0.0"
OCI_APPLICATION="parity-oci-config"
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
)

TRACKING_APPLICATIONS=(
  parity-tracking
  parity-helm-crds-default
  parity-oci-config
)

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
[[ -d "${PROJECT_POLICY_REPO_PATH}" ]] || fail "project policy fixture repo not found: ${PROJECT_POLICY_REPO_PATH}"
[[ -f "${PROJECT_POLICY_EXPECTED}" ]] || fail "project policy expected file not found: ${PROJECT_POLICY_EXPECTED}"

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
  local logs_dir
  logs_dir="$(artifact_dir logs)"
  kubectl -n argocd logs deployment/argocd-repo-server --tail=300 > "${logs_dir}/argocd-repo-server.log" 2>&1 || true
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

prepare_fixture_git_image() {
  local git_work bare image dockerfile
  git_work="${WORK_DIR}/fixture-src"
  bare="${WORK_DIR}/repo.git"
  image="drydock-argocd-parity-git:${CLUSTER_NAME}"
  mkdir -p "${git_work}"
  cp -R "${FIXTURE_REPO_PATH}/." "${git_work}/"
  cp -R "${PROJECT_POLICY_REPO_PATH}/." "${git_work}/"
  git -C "${git_work}" init --initial-branch=main >/dev/null
  git -C "${git_work}" config user.email "drydock@example.invalid"
  git -C "${git_work}" config user.name "drydock render parity smoke"
  git -C "${git_work}" add .
  git -C "${git_work}" commit -m "seed argocd parity fixture" >/dev/null
  git clone --bare --no-hardlinks "${git_work}" "${bare}" >/dev/null
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
  docker build -q -t "${image}" -f "${dockerfile}" "${WORK_DIR}" >/dev/null
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

prepare_registry_image() {
  local image="drydock-argocd-parity-registry:${CLUSTER_NAME}"
  docker pull "${OCI_REGISTRY_IMAGE}" >/dev/null \
    || fail "docker pull of the pinned OCI registry image ${OCI_REGISTRY_IMAGE} failed"
  # A digest pull has no tag; the Deployment matches on the image field with
  # imagePullPolicy Never, so re-tag to the exact name:tag it references.
  docker tag "${OCI_REGISTRY_IMAGE}" "${image}" \
    || fail "docker tag of the pinned OCI registry image to ${image} failed"
  if ! kind load docker-image "${image}" --name "${CLUSTER_NAME}"; then
    # Under Docker's containerd image store a digest pull keeps the multi-arch
    # index but only the host platform's blobs; kind's `ctr images import
    # --all-platforms` of the docker-save stream then fails on the missing
    # foreign-platform manifests ("content digest ...: not found"). Exporting
    # just the host platform sidesteps the index entirely.
    local arch archive
    arch="$(uname -m)"
    case "${arch}" in
      x86_64 | amd64) arch="amd64" ;;
      arm64 | aarch64) arch="arm64" ;;
      *) fail "unsupported host architecture for single-platform registry image export: ${arch}" ;;
    esac
    archive="${WORK_DIR}/registry-image.tar"
    docker save --platform "linux/${arch}" "${image}" -o "${archive}" \
      || fail "single-platform docker save of ${image} for linux/${arch} failed"
    kind load image-archive "${archive}" --name "${CLUSTER_NAME}" \
      || fail "kind load of the OCI registry image ${image} into cluster ${CLUSTER_NAME} failed"
  fi
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
  local sync_status
  for _ in {1..120}; do
    sync_status="$(kubectl -n argocd get application "${OCI_APPLICATION}" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
    if [[ "${sync_status}" == "Synced" || "${sync_status}" == "OutOfSync" ]]; then
      return 0
    fi
    sleep 2
  done
  kubectl -n argocd get application "${OCI_APPLICATION}" -o yaml > "${OUT_DIR}/oci-application.yaml" 2>&1 || true
  fail "OCI Application ${OCI_APPLICATION} did not reach a comparable sync state (Argo CD could not fetch the artifact from ${OCI_REGISTRY_HOST}; check the argocd-tls-certs-cm CA and registry service); see ${OUT_DIR}/oci-application.yaml"
}

warm_drydock_oci_cache() {
  local stderr_file="${OUT_DIR}/drydock-oci-warm.stderr"
  (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${OCI_APPLICATION}" \
    --path "${FIXTURE_REPO_PATH}" \
    --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
    --oci-cache-dir "${OCI_CACHE_DIR}" \
    --oci-ca-file "${OCI_CA_FILE}" \
    --render-cache-dir "${OCI_WARM_RENDER_CACHE_DIR}" \
    > /dev/null 2> "${stderr_file}") \
    || fail "drydock OCI cache warm (non-offline build of ${OCI_APPLICATION} with --oci-cache-dir/--oci-ca-file) failed; see ${stderr_file}"
  rm -f "${stderr_file}"
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
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applications" >/dev/null
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applicationsets" >/dev/null
}

wait_for_applications() {
  local app
  for app in "${APPLICATIONS[@]}"; do
    for _ in {1..120}; do
      if kubectl -n argocd get application "${app}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    kubectl -n argocd get application "${app}" >/dev/null
  done
}

assert_application_inventory() {
  local expected actual
  expected="$(printf '%s\n' "${APPLICATIONS[@]}" | sort)"
  actual="$(kubectl -n argocd get applications.argoproj.io -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)"
  if [[ "${actual}" != "${expected}" ]]; then
    {
      echo "expected Applications:"
      printf '%s\n' "${expected}"
      echo
      echo "actual Applications:"
      printf '%s\n' "${actual}"
    } > "${OUT_DIR}/application-inventory.diff"
    fail "Argo CD Application inventory did not match expected list; see ${OUT_DIR}/application-inventory.diff"
  fi
}

capture_argocd_manifests() {
  local app output_dir
  output_dir="$(artifact_dir argocd-manifests)"
  for app in "${APPLICATIONS[@]}"; do
    for _ in {1..120}; do
      if argocd app manifests "${app}" > "${output_dir}/${app}.yaml" 2> "${OUT_DIR}/argocd-${app}.stderr"; then
        rm -f "${OUT_DIR}/argocd-${app}.stderr"
        break
      fi
      sleep 2
    done
    if [[ ! -s "${output_dir}/${app}.yaml" ]]; then
      fail "Argo CD did not generate manifests for ${app}; see ${OUT_DIR}/argocd-${app}.stderr"
    fi
  done
}

capture_drydock_manifests() {
  local app output_dir
  output_dir="$(artifact_dir drydock-manifests)"
  for app in "${APPLICATIONS[@]}"; do
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
      --path "${FIXTURE_REPO_PATH}" \
      --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
      --oci-cache-dir "${OCI_CACHE_DIR}" \
      --oci-ca-file "${OCI_CA_FILE}" \
      --render-cache-dir "${OCI_OFFLINE_RENDER_CACHE_DIR}" \
      --offline > "${output_dir}/${app}.yaml" 2> "${OUT_DIR}/drydock-${app}.stderr")
    rm -f "${OUT_DIR}/drydock-${app}.stderr"
  done
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

configure_project_policy_application_namespaces() {
  kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge \
    -p '{"data":{"application.namespaces":"project-policy-tenant"}}' >/dev/null
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
  log_step "Configuring Argo CD project-policy Application namespaces"
  configure_project_policy_application_namespaces
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
  log_step "Installing Argo CD ${argocd_version}"
  install_argocd "${argocd_version}"
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
  log_step "Warming drydock OCI artifact cache"
  warm_drydock_oci_cache
  log_step "Capturing drydock rendered manifests"
  capture_drydock_manifests
  compare_manifests
  compare_tracking_manifests
  if [[ "${RUN_PROJECT_POLICY_SMOKE}" == "true" ]]; then
    run_project_policy_smoke
  else
    log_step "Skipping project-policy smoke"
  fi
  echo "Argo CD render parity smoke complete: ${OUT_DIR}" >&2
}

main
