#!/usr/bin/env bash
set -euo pipefail

HOME_OPS_ROOT="${HOME_OPS_ROOT:-/Users/ethan.shold/git/home-ops}"
RENOVATE_CHART_FROM="${RENOVATE_CHART_FROM:-4.8.0}"
RENOVATE_CHART_TO="${RENOVATE_CHART_TO:-4.8.1}"

ROOT="${HOME_OPS_ROOT}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_DIR=""
BASELINE=""
CURRENT=""

cleanup() {
  set +e
  if [[ -n "${BASELINE}" && -d "${BASELINE}" ]]; then
    git -C "${ROOT}" worktree remove --force "${BASELINE}" >/dev/null 2>&1
  fi
  if [[ -n "${CURRENT}" && -d "${CURRENT}" ]]; then
    git -C "${ROOT}" worktree remove --force "${CURRENT}" >/dev/null 2>&1
  fi
  if [[ -n "${TMP_DIR}" && -d "${TMP_DIR}" ]]; then
    rm -rf "${TMP_DIR}"
  fi
}
trap cleanup EXIT

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

before_checksum="$(cksum "${KUSTOMIZATION}")"
perl -0pi.bak -e 'BEGIN { $from = shift @ARGV; $to = shift @ARGV; } s/\Q$from\E/$to/g;' \
  "version: ${RENOVATE_CHART_FROM}" \
  "version: ${RENOVATE_CHART_TO}" \
  "${KUSTOMIZATION}"
rm -f "${KUSTOMIZATION}.bak"
after_checksum="$(cksum "${KUSTOMIZATION}")"

if [[ "${before_checksum}" == "${after_checksum}" ]]; then
  echo "No Renovate chart version changed in ${KUSTOMIZATION}; expected version: ${RENOVATE_CHART_FROM}." >&2
  exit 2
fi

cd "${REPO_ROOT}"
go run ./cmd/argocd-local diff apps --path-orig "${BASELINE}" --path "${CURRENT}" --changed-only=true --exit-code=false
