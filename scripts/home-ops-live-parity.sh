#!/usr/bin/env bash
# Live Argo CD render-parity sweep for the home-ops fleet.
#
# Compares `drydock build app <app>` against Argo CD's OWN render of the same app, captured
# app by app, using drydock's canonicalize+compare engine (scripts/argocd-parity-compare +
# testdata/argocd-parity/compare-ignore.yaml). Run before cutting a drydock release to catch
# render divergences against the real fleet that the synthetic parity fixtures may not cover.
#
# Oracle: rather than `argocd app manifests` (which on this cluster reads a Dragonfly-backed
# managed-resources cache — --maxmemory=256mb --cache_mode=true — that LRU-evicts fleet
# entries and returns "cache: key is missing"), we replicate Argo CD's repo-server command
# locally and cache-free: `kustomize build` for git apps, `helm template` for chart apps.
# kustomize v5.8.1 matches Argo CD's bundled version.
#
# Prerequisites (the script verifies them and fails fast if missing):
#   1. kubectl context for the home-ops cluster (default: "default"). The script pins
#      this context explicitly and NEVER uses the current-context, so it cannot touch
#      other clusters in your kubeconfig. Used only to READ Application specs + sync state.
#   2. kustomize + helm + jq + yq on PATH — the oracle renders each app locally (no argocd
#      login or server-side cache needed).
#   3. A local checkout of home-ops (default: ~/git/home-ops) on any branch/dirty state —
#      the script renders each git-sourced app at the exact revision Argo CD has synced,
#      in a throwaway clean worktree, so your working state does not affect results.
#
# Secrets: both sides are rendered/filtered WITHOUT Secret resources (drydock --skip-secrets;
# oracle output filtered) so no Secret material is written to disk and the comparison is
# apples-to-apples. Secret render parity is intentionally NOT validated here.
#
# Capabilities: BOTH sides render with the same cluster capability set (DRYDOCK_KUBE_VERSION +
# DRYDOCK_API_VERSIONS_FILE → drydock --kube-version/--api-versions and kustomize/helm
# --helm-kube-version/--helm-api-versions), so capability-gated resources (ServiceMonitors etc.)
# appear on both sides and the comparison isolates render fidelity. Without those env vars the
# oracle and drydock both fall back to embedded helm defaults (still apples-to-apples, but
# under-rendering capability-gated resources relative to the live cluster).
set -euo pipefail

KUBE_CONTEXT="${KUBE_CONTEXT:-default}"
ARGOCD_NS="${ARGOCD_NS:-argocd}"
HOME_OPS_ROOT="${HOME_OPS_ROOT:-${HOME}/git/home-ops}"
HOME_OPS_REPO_URL="${HOME_OPS_REPO_URL:-https://github.com/sholdee/home-ops}"
# Render the apps/argocd kustomization to discover the k3s-apps ApplicationSet (git
# directory generator over apps/*) and literal Applications, so drydock finds every
# app in namespace "argocd" exactly as the live cluster does.
DISCOVER_KUSTOMIZE="${DISCOVER_KUSTOMIZE:-apps/argocd}"
# Cluster capabilities to match live Argo CD's capability-gated renders (ServiceMonitors,
# etc.). DRYDOCK_KUBE_VERSION = cluster server version (e.g. 1.35.5); DRYDOCK_API_VERSIONS_FILE
# = file with one group/version or group/version/Kind per line, from
#   { kubectl api-resources --no-headers | awk 'NF>=4{print $(NF-2)"/"$NF}'; kubectl api-versions; } | sort -u
# Both optional; when unset, drydock uses the embedded helm defaults (under-renders
# capability-gated resources, which then surface as "capability-gated" diffs).
KUBE_VERSION="${DRYDOCK_KUBE_VERSION:-}"
API_VERSIONS_FILE="${DRYDOCK_API_VERSIONS_FILE:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
IGNORE_FILE="${IGNORE_FILE:-${REPO_ROOT}/testdata/argocd-parity/compare-ignore.yaml}"
OUT_DIR="${OUT_DIR:-${REPO_ROOT}/home-ops-live-parity}"
DRYDOCK_BIN=""
ONLY_APPS=()

usage() {
  cat <<EOF
Usage: scripts/home-ops-live-parity.sh [options]
  --app <name>        Restrict to this app (repeatable; default: all live apps)
  --binary <path>     Use a prebuilt drydock binary (default: build one into a temp dir)
  --context <name>    kube context for the home-ops cluster (default: ${KUBE_CONTEXT})
  --home-ops <path>   Local home-ops checkout (default: ${HOME_OPS_ROOT})
  --out-dir <path>    Canonical manifests + diffs output (default: ${OUT_DIR})
  -h, --help          Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --app) ONLY_APPS+=("$2"); shift 2 ;;
    --binary) DRYDOCK_BIN="$2"; shift 2 ;;
    --context) KUBE_CONTEXT="$2"; shift 2 ;;
    --home-ops) HOME_OPS_ROOT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

command -v kubectl   >/dev/null || { echo "kubectl not found" >&2; exit 1; }
command -v kustomize >/dev/null || { echo "kustomize not found (oracle renders git apps via 'kustomize build')" >&2; exit 1; }
command -v helm      >/dev/null || { echo "helm not found (oracle renders chart apps via 'helm template')" >&2; exit 1; }
command -v jq        >/dev/null || { echo "jq not found (needed to read Application source specs)" >&2; exit 1; }
command -v yq        >/dev/null || { echo "yq not found (needed to filter Secret resources)" >&2; exit 1; }
[[ -d "${HOME_OPS_ROOT}/.git" ]] || { echo "home-ops checkout not found at ${HOME_OPS_ROOT}" >&2; exit 1; }

kc() { kubectl --context "${KUBE_CONTEXT}" -n "${ARGOCD_NS}" "$@"; }

# Fail fast on access prerequisites.
kc get applications >/dev/null 2>&1 || { echo "cannot list Applications via kube context '${KUBE_CONTEXT}' ns '${ARGOCD_NS}'" >&2; exit 1; }

WORK="$(mktemp -d)"
ARGOCD_DIR="${WORK}/argocd"
DRYDOCK_DIR="${WORK}/drydock"
CACHE="${WORK}/cache"
mkdir -p "${ARGOCD_DIR}" "${DRYDOCK_DIR}" "${CACHE}"/{git,charts,remotes,render}
WORKTREES=()

cleanup() {
  local wt
  for wt in "${WORKTREES[@]:-}"; do
    [[ -n "${wt}" ]] && git -C "${HOME_OPS_ROOT}" worktree remove --force "${wt}" 2>/dev/null || true
  done
  git -C "${HOME_OPS_ROOT}" worktree prune 2>/dev/null || true
  rm -rf "${WORK}"
}
trap cleanup EXIT

# Build drydock once unless a binary was supplied.
if [[ -z "${DRYDOCK_BIN}" ]]; then
  DRYDOCK_BIN="${WORK}/drydock-bin"
  echo "==> Building drydock"
  (cd "${REPO_ROOT}" && go build -o "${DRYDOCK_BIN}" ./cmd/drydock)
fi

# Enumerate apps.
if [[ ${#ONLY_APPS[@]} -gt 0 ]]; then
  APPS=("${ONLY_APPS[@]}")
else
  mapfile -t APPS < <(kc get applications -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
fi
echo "==> ${#APPS[@]} application(s) to compare (context: ${KUBE_CONTEXT})"

# Determine the home-ops git revision Argo CD has synced (most common among
# git-sourced apps) and check out a clean worktree there so drydock renders the
# same content Argo CD did.
SYNCED_REV="$(kc get applications -o jsonpath="{range .items[?(@.spec.source.repoURL==\"${HOME_OPS_REPO_URL}\")]}{.status.sync.revision}{\"\n\"}{end}" | sort | uniq -c | sort -rn | awk 'NR==1{print $2}')"
if [[ -z "${SYNCED_REV}" ]]; then
  echo "could not determine a synced home-ops git revision" >&2; exit 1
fi
echo "==> home-ops synced revision: ${SYNCED_REV}"
echo "==> Fetching home-ops and creating clean worktree at synced revision"
git -C "${HOME_OPS_ROOT}" fetch --quiet origin "${SYNCED_REV}" 2>/dev/null || git -C "${HOME_OPS_ROOT}" fetch --quiet origin || true
WT="${WORK}/home-ops-worktree"
git -C "${HOME_OPS_ROOT}" worktree add --quiet --detach "${WT}" "${SYNCED_REV}"
WORKTREES+=("${WT}")
# The oracle's `kustomize build --enable-helm` pulls charts INTO <kustomization>/charts/,
# which would pollute the worktree drydock discovers from (e.g. apps/argocd/charts/argo-cd-*
# breaks `build app`'s --discover-kustomize for every later app). Give the oracle its OWN
# worktree so the two renders never share on-disk state.
WT_ORACLE="${WORK}/home-ops-oracle-worktree"
git -C "${HOME_OPS_ROOT}" worktree add --quiet --detach "${WT_ORACLE}" "${SYNCED_REV}"
WORKTREES+=("${WT_ORACLE}")

# Build the capability flags drydock should render with, so capability-gated resources
# match the live cluster (otherwise they show up as benign "capability-gated" diffs).
CAP_ARGS=()
[[ -n "${KUBE_VERSION}" ]] && CAP_ARGS+=(--kube-version "${KUBE_VERSION}")
if [[ -n "${API_VERSIONS_FILE}" && -f "${API_VERSIONS_FILE}" ]]; then
  while IFS= read -r av; do
    [[ -n "${av}" ]] && CAP_ARGS+=(--api-versions "${av}")
  done < "${API_VERSIONS_FILE}"
fi
echo "==> Capability flags: kube-version=${KUBE_VERSION:-<default>}, api-versions=$([[ -n ${API_VERSIONS_FILE} ]] && wc -l < "${API_VERSIONS_FILE}" || echo 0) entries"

# Oracle capability args: kustomize's --enable-helm shells out to helm, so git apps take
# --helm-kube-version/--helm-api-versions; chart apps (helm template) take --kube-version/--api-versions.
# Both derive from the SAME DRYDOCK_* inputs drydock gets, so both sides share one capability set.
KZ_HELM_CAP=(--enable-helm)
HELM_CAP=()
[[ -n "${KUBE_VERSION}" ]] && { KZ_HELM_CAP+=(--helm-kube-version "${KUBE_VERSION}"); HELM_CAP+=(--kube-version "${KUBE_VERSION}"); }
if [[ -n "${API_VERSIONS_FILE}" && -f "${API_VERSIONS_FILE}" ]]; then
  while IFS= read -r av; do
    [[ -z "${av}" ]] && continue
    KZ_HELM_CAP+=(--helm-api-versions "${av}")
    HELM_CAP+=(--api-versions "${av}")
  done < "${API_VERSIONS_FILE}"
fi

# Render Argo CD's desired state for ONE app, Secret-free, WITHOUT the managed-resources cache,
# by replicating the repo-server's command from the Application's source spec:
#   git + kustomize   -> kustomize build <worktree>/<path> --enable-helm <caps>
#   remote helm chart -> helm template <release> <chartref> --version <v> -n <ns> --include-crds <caps> -f <valuesObject>
# (Every home-ops chart app uses inline spec.source.helm.valuesObject only — no valueFiles/parameters.)
# Called as `if ! render_oracle ...`, so set -e is suspended inside; failures are returned explicitly.
render_oracle() {
  local app="$1" out="$2" spec path chart repo ver ns release vals
  spec="$(kc get application "${app}" -o json 2>>"${WORK}/argocd-${app}.err")" || return 1
  path="$(jq -r '.spec.source.path // ""'                <<<"${spec}")"
  chart="$(jq -r '.spec.source.chart // ""'              <<<"${spec}")"
  repo="$(jq -r '.spec.source.repoURL // ""'             <<<"${spec}")"
  ver="$(jq -r '.spec.source.targetRevision // ""'       <<<"${spec}")"
  ns="$(jq -r '.spec.destination.namespace // ""'        <<<"${spec}")"
  release="$(jq -r '.spec.source.helm.releaseName // ""' <<<"${spec}")"; [[ -z "${release}" ]] && release="${app}"
  if [[ -n "${chart}" ]]; then
    vals="$(mktemp)"; jq '.spec.source.helm.valuesObject // {}' <<<"${spec}" > "${vals}"  # helm -f reads JSON as YAML
    local -a hcmd
    if   [[ "${repo}" == oci://* ]];   then hcmd=(helm template "${release}" "${repo}/${chart}"         --version "${ver}")
    elif [[ "${repo}" == http*://* ]]; then hcmd=(helm template "${release}" "${chart}" --repo "${repo}" --version "${ver}")
    else                                    hcmd=(helm template "${release}" "oci://${repo}/${chart}"   --version "${ver}")  # ghcr.io/... OCI
    fi
    hcmd+=(-n "${ns}" --include-crds "${HELM_CAP[@]}" -f "${vals}")
    "${hcmd[@]}" 2>>"${WORK}/argocd-${app}.err" | yq 'select(.kind != "Secret")' > "${out}" 2>>"${WORK}/argocd-${app}.err"
    local rc=${PIPESTATUS[0]}; rm -f "${vals}"; [[ ${rc} -eq 0 ]] || return 1
  elif [[ -n "${path}" ]]; then
    kustomize build "${WT_ORACLE}/${path}" "${KZ_HELM_CAP[@]}" 2>>"${WORK}/argocd-${app}.err" \
      | yq 'select(.kind != "Secret")' > "${out}" 2>>"${WORK}/argocd-${app}.err"
    [[ ${PIPESTATUS[0]} -eq 0 ]] || return 1
  else
    echo "Application has neither source.path nor source.chart" >>"${WORK}/argocd-${app}.err"; return 1
  fi
  [[ -s "${out}" ]]
}

# Capture both renders per app, Secret-free.
echo "==> Capturing Argo CD (oracle) and drydock manifests"
failed=()
for app in "${APPS[@]}"; do
  # Argo CD desired-state render (the oracle), Secret-free, via local cache-free replication.
  if ! render_oracle "${app}" "${ARGOCD_DIR}/${app}.yaml"; then
    echo "  ! oracle render ${app} failed (see ${WORK}/argocd-${app}.err)"
    failed+=("${app}:argocd")
    continue
  fi
  # drydock render of the same Application, at the synced revision, Secret-free.
  if ! "${DRYDOCK_BIN}" build app "argocd/${app}" \
        --path "${WT}" \
        --repo-map "${HOME_OPS_REPO_URL}=${WT}" \
        --discover-kustomize "${DISCOVER_KUSTOMIZE}" \
        --skip-secrets \
        "${CAP_ARGS[@]}" \
        --git-cache-dir "${CACHE}/git" --chart-cache-dir "${CACHE}/charts" \
        --remote-cache-dir "${CACHE}/remotes" --render-cache-dir "${CACHE}/render" \
        -o yaml > "${DRYDOCK_DIR}/${app}.yaml" 2>"${WORK}/drydock-${app}.err"; then
    echo "  ! drydock build app argocd/${app} failed (see ${WORK}/drydock-${app}.err)"
    failed+=("${app}:drydock")
    continue
  fi
done

# Compare all apps at once with the canonicalize+ignore engine.
echo "==> Comparing (canonical, ignore rules: ${IGNORE_FILE})"
rm -rf "${OUT_DIR}"; mkdir -p "${OUT_DIR}"
set +e
(cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
  --argocd-dir "${ARGOCD_DIR}" \
  --drydock-dir "${DRYDOCK_DIR}" \
  --out-dir "${OUT_DIR}" \
  --ignore-file "${IGNORE_FILE}")
compare_rc=$?
set -e

echo
echo "==> Summary"
echo "    apps compared: ${#APPS[@]}"
echo "    capture failures: ${#failed[@]} ${failed[*]:-}"
echo "    canonical manifests + diffs: ${OUT_DIR}"
if [[ ${compare_rc} -ne 0 ]]; then
  echo "    RESULT: differences found (compare exit ${compare_rc}) — review ${OUT_DIR} and triage:"
  echo "            (a) capability-gated (ServiceMonitor etc.) — should be EMPTY now both sides get the same --api-versions"
  echo "            (b) content drift (a chart/app pinned newer in git than argocd has synced)"
  echo "            (c) helm-version serialization (local helm vs argocd's bundled helm v3.19.4 — item C territory)"
  echo "            (d) real render divergences — these are the ones to fix before release"
else
  echo "    RESULT: 0 differences — drydock matches the cache-free repo-server render across the fleet."
fi
[[ ${#failed[@]} -eq 0 && ${compare_rc} -eq 0 ]]
