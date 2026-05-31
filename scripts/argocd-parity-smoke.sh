#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ARGOCD_MODULE="github.com/argoproj/argo-cd/v3"
FIXTURE_REPO_URL="git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git"
FIXTURE_REPO_PATH="${REPO_ROOT}/testdata/argocd-parity/repo"
IGNORE_FILE="${REPO_ROOT}/testdata/argocd-parity/compare-ignore.yaml"
OUT_DIR="${REPO_ROOT}/argocd-parity-smoke"
KEEP_CLUSTER="false"
CLUSTER_NAME="drydock-argocd-parity-${GITHUB_RUN_ID:-$$}"
DRYDOCK_CMD=(go run ./cmd/drydock)
PORT_FORWARD_PID=""

APPLICATIONS=(
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
)

TRACKING_APPLICATIONS=(
  parity-tracking
)

usage() {
  cat <<'USAGE'
Usage: scripts/argocd-parity-smoke.sh [options]

Options:
  --binary <path>        drydock binary to run instead of go run ./cmd/drydock
  --out <dir>           output artifact directory (default: ./argocd-parity-smoke)
  --cluster-name <name> kind cluster name
  --keep-cluster        leave the kind cluster running for debugging
  -h, --help            show this help
USAGE
}

fail() {
  echo "argocd parity smoke: $*" >&2
  exit 2
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

for tool in base64 curl docker git go kind kubectl; do
  require_tool "${tool}"
done

[[ -d "${FIXTURE_REPO_PATH}" ]] || fail "fixture repo not found: ${FIXTURE_REPO_PATH}"
[[ -f "${IGNORE_FILE}" ]] || fail "compare ignore file not found: ${IGNORE_FILE}"

OUT_DIR="$(mkdir -p "${OUT_DIR}" && cd "${OUT_DIR}" && pwd)"
WORK_DIR="$(mktemp -d)"
ARGOCD_CONFIG_DIR="${WORK_DIR}/argocd"
ARGOCD_CONFIG="${ARGOCD_CONFIG_DIR}/config"
mkdir -p "${ARGOCD_CONFIG_DIR}"
chmod 0700 "${ARGOCD_CONFIG_DIR}"
export ARGOCD_CONFIG

cleanup() {
  local status="$?"
  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
  fi
  if [[ "${status}" -ne 0 ]]; then
    collect_logs || true
  fi
  rm -rf "${WORK_DIR}"
  if [[ "${KEEP_CLUSTER}" != "true" ]]; then
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
  git -C "${git_work}" init --initial-branch=main >/dev/null
  git -C "${git_work}" config user.email "drydock@example.invalid"
  git -C "${git_work}" config user.name "drydock parity smoke"
  git -C "${git_work}" add .
  git -C "${git_work}" commit -m "seed argocd parity fixture" >/dev/null
  git clone --bare "${git_work}" "${bare}" >/dev/null
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

install_argocd() {
  local version="$1"
  kubectl create namespace argocd >/dev/null
  kubectl -n argocd apply --server-side --force-conflicts -f "https://raw.githubusercontent.com/argoproj/argo-cd/${version}/manifests/install.yaml" >/dev/null
  kubectl wait --for=condition=Established crd/applications.argoproj.io --timeout=120s
  kubectl wait --for=condition=Established crd/applicationsets.argoproj.io --timeout=120s
  kubectl -n argocd patch configmap argocd-cm --type merge -p '{"data":{"kustomize.buildOptions":"--enable-helm"}}' >/dev/null
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
      --offline > "${output_dir}/${app}.yaml" 2> "${OUT_DIR}/drydock-${app}.stderr")
    rm -f "${OUT_DIR}/drydock-${app}.stderr"
  done
}

compare_manifests() {
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${OUT_DIR}/argocd-manifests" \
    --drydock-dir "${OUT_DIR}/drydock-manifests" \
    --out-dir "${OUT_DIR}/compare" \
    --ignore-file "${IGNORE_FILE}")
}

compare_tracking_manifests() {
  local app argocd_dir drydock_dir
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

main() {
  local argocd_version
  argocd_version="$(resolve_argocd_version)"
  echo "Argo CD parity smoke: ${argocd_version}" >&2
  install_argocd_cli "${argocd_version}"
  kind create cluster --name "${CLUSTER_NAME}"
  prepare_fixture_git_image
  install_fixture_git_server
  install_argocd "${argocd_version}"
  login_argocd
  apply_fixture_apps
  wait_for_applications
  assert_application_inventory
  capture_argocd_manifests
  capture_drydock_manifests
  compare_manifests
  compare_tracking_manifests
  echo "Argo CD parity smoke complete: ${OUT_DIR}" >&2
}

main
