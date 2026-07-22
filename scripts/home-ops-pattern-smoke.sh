#!/usr/bin/env bash
set -euo pipefail

HOME_OPS_ROOT="${HOME_OPS_ROOT:-${HOME}/git/home-ops}"

ROOT="${HOME_OPS_ROOT}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_DIR=""
BASELINE=""
CURRENT=""
REMOTE_CACHE=""

# shellcheck source=scripts/lib/home-ops-smoke.sh
source "${SCRIPT_DIR}/lib/home-ops-smoke.sh"
trap cleanup EXIT

fail() {
  echo "home-ops pattern smoke: $*" >&2
  exit 2
}

require_file() {
  local file="$1"
  [[ -f "${file}" ]] || fail "expected file not found: ${file}"
}

count_literal() {
  local file="$1"
  local needle="$2"
  perl -0ne 'BEGIN { $needle = shift @ARGV } $count += () = /\Q$needle\E/g; END { print $count + 0 }' "${needle}" "${file}"
}

replace_once_literal() {
  local file="$1"
  local from="$2"
  local to="$3"
  local before_count
  local after_from_count
  local after_to_count

  require_file "${file}"
  before_count="$(count_literal "${file}" "${from}")"
  if [[ "${before_count}" != "1" ]]; then
    fail "expected exactly one match in ${file}, found ${before_count}: ${from}"
  fi

  perl -0pi.bak -e 'BEGIN { $from = shift @ARGV; $to = shift @ARGV } s/\Q$from\E/$to/g' "${from}" "${to}" "${file}"
  rm -f "${file}.bak"

  after_from_count="$(count_literal "${file}" "${from}")"
  if [[ "${after_from_count}" != "0" ]]; then
    fail "replacement left ${after_from_count} old matches in ${file}: ${from}"
  fi
  after_to_count="$(count_literal "${file}" "${to}")"
  if [[ "${after_to_count}" != "1" ]]; then
    fail "replacement produced ${after_to_count} new matches in ${file}: ${to}"
  fi
}

previous_semver() {
  local version="$1"
  local major
  local minor
  local patch

  IFS=. read -r major minor patch <<<"${version}"
  if [[ ! "${major}" =~ ^[0-9]+$ || ! "${minor}" =~ ^[0-9]+$ || ! "${patch}" =~ ^[0-9]+$ ]]; then
    fail "cannot derive previous semver from ${version}; set an explicit chart target override"
  fi
  if (( patch > 0 )); then
    patch=$((patch - 1))
  elif (( minor > 0 )); then
    minor=$((minor - 1))
  else
    fail "cannot derive previous semver from ${version}; set an explicit chart target override"
  fi
  printf '%s.%s.%s\n' "${major}" "${minor}" "${patch}"
}

update_helm_chart_version_once() {
  local file="$1"
  local chart_name="$2"
  local version_to="$3"
  local output="${file}.tmp"

  require_file "${file}"
  update_helm_chart_version "${file}" "${chart_name}" "${version_to}" "${output}" 1 || fail "failed to update chart ${chart_name} in ${file}"
  mv "${output}" "${file}"
}

restore_path() {
  local path="$1"
  git -C "${CURRENT}" checkout -- "${path}"
}

run_diff() {
  local label="$1"
  echo "== ${label} =="
  (
    cd "${REPO_ROOT}"
    go run ./cmd/drydock diff apps --path-orig "${BASELINE}" --path "${CURRENT}" --remote-cache-dir "${REMOTE_CACHE}" --changed-only=true --exit-code=false
  )
}

if [[ ! -e "${ROOT}/.git" ]]; then
  fail "home-ops checkout not found at ${ROOT}; set HOME_OPS_ROOT to a local checkout"
fi

TMP_DIR="$(mktemp -d)"
BASELINE="${TMP_DIR}/baseline"
CURRENT="${TMP_DIR}/current"
REMOTE_CACHE="${TMP_DIR}/remote-cache"

git -C "${ROOT}" worktree add --detach "${BASELINE}" HEAD
git -C "${ROOT}" worktree add --detach "${CURRENT}" HEAD

ADGUARD_DEPLOYMENT="apps/adguard/manifests/deployment.yaml"
KROMGO_CONFIG="apps/monitoring/kromgo/manifests/config.yaml"
RENOVATE_KUSTOMIZATION="apps/renovate/kustomization.yaml"
EXTERNAL_SECRETS_KUSTOMIZATION="apps/external-secrets/kustomization.yaml"

replace_once_literal \
  "${CURRENT}/${ADGUARD_DEPLOYMENT}" \
  "adguard/adguardhome:v0.107.76@sha256:7157eb1dc3b26c7af1d6898759a7b3f7d0fa09891fbd2d3caa6abc1057a9179b" \
  "adguard/adguardhome:v0.107.76-pattern-smoke@sha256:7157eb1dc3b26c7af1d6898759a7b3f7d0fa09891fbd2d3caa6abc1057a9179b"
run_diff "plain resource edit"

restore_path "${ADGUARD_DEPLOYMENT}"
replace_once_literal \
  "${CURRENT}/${KROMGO_CONFIG}" \
  "query: count(kube_node_info)" \
  "query: count(kube_node_status_condition)"
run_diff "configMapGenerator edit"

restore_path "${KROMGO_CONFIG}"
RENOVATE_CURRENT_VERSION="$(detect_helm_chart_version "${CURRENT}/${RENOVATE_KUSTOMIZATION}" "renovate-operator")"
RENOVATE_TARGET_VERSION="${RENOVATE_CHART_TO:-$(previous_semver "${RENOVATE_CURRENT_VERSION}")}"
if [[ "${RENOVATE_TARGET_VERSION}" == "${RENOVATE_CURRENT_VERSION}" ]]; then
  fail "RENOVATE_CHART_TO matches current version ${RENOVATE_CURRENT_VERSION}"
fi
update_helm_chart_version_once "${CURRENT}/${RENOVATE_KUSTOMIZATION}" "renovate-operator" "${RENOVATE_TARGET_VERSION}"
run_diff "OCI Helm chart version edit"

restore_path "${RENOVATE_KUSTOMIZATION}"
EXTERNAL_SECRETS_CURRENT_VERSION="$(detect_helm_chart_version "${CURRENT}/${EXTERNAL_SECRETS_KUSTOMIZATION}" "external-secrets")"
EXTERNAL_SECRETS_TARGET_VERSION="${EXTERNAL_SECRETS_CHART_TO:-$(previous_semver "${EXTERNAL_SECRETS_CURRENT_VERSION}")}"
if [[ "${EXTERNAL_SECRETS_TARGET_VERSION}" == "${EXTERNAL_SECRETS_CURRENT_VERSION}" ]]; then
  fail "EXTERNAL_SECRETS_CHART_TO matches current version ${EXTERNAL_SECRETS_CURRENT_VERSION}"
fi
update_helm_chart_version_once "${CURRENT}/${EXTERNAL_SECRETS_KUSTOMIZATION}" "external-secrets" "${EXTERNAL_SECRETS_TARGET_VERSION}"
run_diff "multiple Helm charts edit"

restore_path "${EXTERNAL_SECRETS_KUSTOMIZATION}"
SYSTEM_UPGRADE_PLAN="apps/system-upgrade/manifests/plan.yaml"
if [[ ! -f "${CURRENT}/${SYSTEM_UPGRADE_PLAN}" && -f "${CURRENT}/apps/system-upgrade/plan.yaml" ]]; then
  SYSTEM_UPGRADE_PLAN="apps/system-upgrade/plan.yaml"
fi
replace_once_literal \
  "${CURRENT}/${SYSTEM_UPGRADE_PLAN}" \
  "operator: DoesNotExist" \
  "operator: Exists"
run_diff "system-upgrade remote resource"
