#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

PROFILE_MODE="cpu"
PROFILE_OUT="${REPO_ROOT}/drydock-profiles"
REF="HEAD"
REF_ORIG=""
WARM_RUNS="1"
SKIP_DIFF="false"
DRYDOCK_CMD=(go run ./cmd/drydock)
TARGET_REPO=""

usage() {
  cat <<'USAGE'
Usage: scripts/profile-smoke.sh <repo> [options]

Options:
  --binary <path>      drydock binary to run instead of go run ./cmd/drydock
  --profile <mode>    profile mode: cpu, mem, block, mutex, or trace (default: cpu)
  --out <dir>         profile output directory (default: ./drydock-profiles)
  --ref <ref>         current Git ref for diff apps (default: HEAD)
  --ref-orig <ref>    baseline Git ref for diff apps (default: origin/HEAD, main, or master)
  --warm-runs <n>     number of times to run each command (default: 1)
  --skip-diff         skip the ref diff command
  -h, --help          show this help
USAGE
}

fail() {
  echo "profile smoke: $*" >&2
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
    --profile)
      require_value "$1" "${2:-}"
      PROFILE_MODE="$2"
      shift 2
      ;;
    --out)
      require_value "$1" "${2:-}"
      PROFILE_OUT="$2"
      shift 2
      ;;
    --ref)
      require_value "$1" "${2:-}"
      REF="$2"
      shift 2
      ;;
    --ref-orig)
      require_value "$1" "${2:-}"
      REF_ORIG="$2"
      shift 2
      ;;
    --warm-runs)
      require_value "$1" "${2:-}"
      WARM_RUNS="$2"
      shift 2
      ;;
    --skip-diff)
      SKIP_DIFF="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    -*)
      fail "unknown option: $1"
      ;;
    *)
      if [[ -n "${TARGET_REPO}" ]]; then
        fail "unexpected extra argument: $1"
      fi
      TARGET_REPO="$1"
      shift
      ;;
  esac
done

[[ -n "${TARGET_REPO}" ]] || {
  usage >&2
  exit 2
}
[[ -d "${TARGET_REPO}" ]] || fail "repository directory does not exist: ${TARGET_REPO}"
[[ "${WARM_RUNS}" =~ ^[1-9][0-9]*$ ]] || fail "--warm-runs must be a positive integer"

TARGET_REPO="$(cd "${TARGET_REPO}" && pwd)"
PROFILE_OUT="$(mkdir -p "${PROFILE_OUT}" && cd "${PROFILE_OUT}" && pwd)"

detect_ref_orig() {
  local repo="$1"
  local detected=""
  detected="$(git -C "${repo}" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  detected="${detected#origin/}"
  if [[ -n "${detected}" ]]; then
    echo "${detected}"
    return
  fi
  if git -C "${repo}" show-ref --verify --quiet refs/heads/main; then
    echo "main"
    return
  fi
  if git -C "${repo}" show-ref --verify --quiet refs/heads/master; then
    echo "master"
    return
  fi
}

if [[ -z "${REF_ORIG}" && "${SKIP_DIFF}" == "false" ]]; then
  REF_ORIG="$(detect_ref_orig "${TARGET_REPO}")"
  [[ -n "${REF_ORIG}" ]] || fail "could not detect --ref-orig; pass it explicitly or use --skip-diff"
fi

run_drydock() {
  local label="$1"
  shift
  echo "==> ${label}" >&2
  (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" --profile "${PROFILE_MODE}" --profile-out "${PROFILE_OUT}" "$@")
}

run_count=0
while [[ "${run_count}" -lt "${WARM_RUNS}" ]]; do
  run_count=$((run_count + 1))
  echo "profile smoke run ${run_count}/${WARM_RUNS}: ${TARGET_REPO}" >&2
  run_drydock "test apps" test apps --path "${TARGET_REPO}"
  if [[ "${SKIP_DIFF}" == "false" ]]; then
    run_drydock "diff apps ${REF_ORIG}..${REF}" diff apps --repo "${TARGET_REPO}" --ref "${REF}" --ref-orig "${REF_ORIG}" --exit-code=false
  fi
  run_drydock "get images" get images --path "${TARGET_REPO}" -o name
done

echo "profile smoke complete: ${PROFILE_OUT}" >&2
