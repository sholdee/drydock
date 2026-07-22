#!/usr/bin/env bash
set -euo pipefail

HOME_OPS_ROOT="${HOME_OPS_ROOT:-${HOME}/git/home-ops}"
RENOVATE_CHART_NAME="${RENOVATE_CHART_NAME:-renovate-operator}"

ROOT="${HOME_OPS_ROOT}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_DIR=""
BASELINE=""
CURRENT=""

# shellcheck source=scripts/lib/home-ops-smoke.sh
source "${SCRIPT_DIR}/lib/home-ops-smoke.sh"
trap cleanup EXIT

detect_renovate_chart_version() {
  detect_helm_chart_version "$1" "${2:-renovate-operator}"
}

update_renovate_chart_version() {
  update_helm_chart_version "$1" "$2" "$3" "$4"
}

if [[ "${DRYDOCK_SMOKE_LIB_ONLY:-}" == "true" ]]; then
  if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
  fi
  exit 0
fi

if [[ -z "${RENOVATE_CHART_TO:-}" ]]; then
  echo "RENOVATE_CHART_TO is required. Example: RENOVATE_CHART_TO=4.8.2 scripts/home-ops-renovate-smoke.sh" >&2
  exit 2
fi

if [[ ! -e "${ROOT}/.git" ]]; then
  echo "home-ops checkout not found at ${ROOT}; set HOME_OPS_ROOT to a local checkout." >&2
  exit 2
fi

TMP_DIR="$(mktemp -d)"
BASELINE="${TMP_DIR}/baseline"
CURRENT="${TMP_DIR}/current"

git -C "${ROOT}" worktree add --detach "${BASELINE}" HEAD
git -C "${ROOT}" worktree add --detach "${CURRENT}" HEAD

KUSTOMIZATION="${CURRENT}/apps/renovate/kustomization.yaml"
if [[ ! -f "${KUSTOMIZATION}" ]]; then
  echo "Renovate kustomization not found in temp worktree: ${KUSTOMIZATION}" >&2
  exit 2
fi

if ! CURRENT_CHART_VERSION="$(detect_renovate_chart_version "${KUSTOMIZATION}" "${RENOVATE_CHART_NAME}")"; then
  echo "Could not detect ${RENOVATE_CHART_NAME} chart version in ${KUSTOMIZATION}." >&2
  exit 2
fi

if [[ "${CURRENT_CHART_VERSION}" == "${RENOVATE_CHART_TO}" ]]; then
  echo "RENOVATE_CHART_TO is already the current ${RENOVATE_CHART_NAME} chart version: ${CURRENT_CHART_VERSION}." >&2
  exit 2
fi

before_checksum="$(cksum "${KUSTOMIZATION}")"
if ! update_renovate_chart_version "${KUSTOMIZATION}" "${RENOVATE_CHART_NAME}" "${RENOVATE_CHART_TO}" "${KUSTOMIZATION}.tmp"; then
  rm -f "${KUSTOMIZATION}.tmp"
  echo "Could not update ${RENOVATE_CHART_NAME} chart version in ${KUSTOMIZATION}." >&2
  exit 2
fi
mv "${KUSTOMIZATION}.tmp" "${KUSTOMIZATION}"
after_checksum="$(cksum "${KUSTOMIZATION}")"

if [[ "${before_checksum}" == "${after_checksum}" ]]; then
  echo "No Renovate chart version changed in ${KUSTOMIZATION}; detected version: ${CURRENT_CHART_VERSION}." >&2
  exit 2
fi

cd "${REPO_ROOT}"
go run ./cmd/drydock diff apps --path-orig "${BASELINE}" --path "${CURRENT}" --changed-only=true --exit-code=false
