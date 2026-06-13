#!/usr/bin/env bash
set -euo pipefail

bool() {
  case "$1" in
    true | false) ;;
    *)
      echo "$2 must be true or false, got '$1'." >&2
      exit 1
      ;;
  esac
}

positive_int() {
  case "$1" in
    '' ) return 0 ;;
    *[!0-9]* )
      echo "$2 must be a positive integer, got '$1'." >&2
      exit 1
      ;;
    0 )
      echo "$2 must be greater than zero." >&2
      exit 1
      ;;
  esac
}

bool "${DRYDOCK_INPUT_CACHE}" cache
bool "${DRYDOCK_INPUT_SAVE_CACHE}" save-cache
bool "${DRYDOCK_INPUT_CACHE_UNTRUSTED_RESTORE}" cache-untrusted-restore
bool "${DRYDOCK_INPUT_UPLOAD_ARTIFACTS}" upload-artifacts
bool "${DRYDOCK_INPUT_COMMENT_EMPTY}" comment-empty
bool "${DRYDOCK_INPUT_COMMENT_CONTINUE_ON_ERROR}" comment-continue-on-error
positive_int "${DRYDOCK_INPUT_ARTIFACT_RETENTION_DAYS}" artifact-retention-days
positive_int "${DRYDOCK_INPUT_DIFF_MAX_BYTES}" diff-max-bytes

case "${DRYDOCK_INPUT_COMMENT_MODE}" in
  none | diff | images | both) ;;
  *)
    echo "comment-mode must be none, diff, images, or both, got '${DRYDOCK_INPUT_COMMENT_MODE}'." >&2
    exit 1
    ;;
esac

cache_mode="${DRYDOCK_INPUT_CACHE_MODE:-auto}"
case "${cache_mode}" in
  auto | github | local | off) ;;
  *)
    echo "cache-mode must be auto, github, local, or off, got '${cache_mode}'." >&2
    exit 1
    ;;
esac
# Back-compat: cache: false disables persistence regardless of cache-mode.
if [[ "${DRYDOCK_INPUT_CACHE}" != "true" ]]; then
  cache_mode="off"
fi

runner_environment="${RUNNER_ENVIRONMENT:-}"

runner_temp="${RUNNER_TEMP:-/tmp}"
work_dir="$(mktemp -d "${runner_temp%/}/drydock-pr-action.XXXXXX")"
cache_path="${DRYDOCK_INPUT_CACHE_PATH}"
if [[ -z "${cache_path}" ]]; then
  if [[ "${cache_mode}" == "local" ]]; then
    # Prefer the persistent tool cache so a self-hosted runner keeps the cache
    # across jobs; RUNNER_TEMP is cleaned each job.
    cache_root="${RUNNER_TOOL_CACHE:-${runner_temp}}"
    cache_path="${cache_root%/}/drydock-cache"
  else
    cache_path="${runner_temp%/}/drydock-cache"
  fi
fi
mkdir -p "${cache_path}"

trusted_context=true
case "${GITHUB_EVENT_NAME:-}" in
  pull_request | pull_request_target)
    if [[ -n "${DRYDOCK_PR_HEAD_REPO:-}" && "${DRYDOCK_PR_HEAD_REPO}" != "${GITHUB_REPOSITORY:-}" ]]; then
      trusted_context=false
    fi
    ;;
esac

# auto and github use the remote actions/cache backend; local and off do not
# (local persists at cache_path on the runner, off keeps no cache between runs).
cache_restore=false
cache_save=false
case "${cache_mode}" in
  auto | github)
    if [[ "${trusted_context}" == "true" || "${DRYDOCK_INPUT_CACHE_UNTRUSTED_RESTORE}" == "true" ]]; then
      cache_restore=true
    fi
    if [[ "${trusted_context}" == "true" && "${DRYDOCK_INPUT_SAVE_CACHE}" == "true" ]]; then
      cache_save=true
    fi
    ;;
  local | off) ;;
esac

# Guidance and guardrails keyed on the runner environment. runner.environment
# distinguishes github-hosted from self-hosted, but not whether a self-hosted
# runner has a persistent filesystem, so the default never switches modes on
# its own.
if [[ "${cache_mode}" == "auto" && "${runner_environment}" == "self-hosted" ]]; then
  echo "::notice::drydock: self-hosted runner detected. If this runner has persistent storage, set cache-mode: local with a persistent cache-path to skip actions/cache round-trips and the cross-pull-request cache scope limits."
fi
if [[ "${cache_mode}" == "local" && "${runner_environment}" == "github-hosted" ]]; then
  echo "::warning::drydock: cache-mode: local does not persist on a GitHub-hosted runner (its filesystem is ephemeral); use cache-mode: auto or github."
fi
if [[ "${cache_mode}" == "local" && "${cache_path}" == "${runner_temp%/}"/* ]]; then
  echo "::warning::drydock: cache-mode: local cache-path is under RUNNER_TEMP, which is cleaned each job and will not persist; set cache-path to a directory that survives across jobs."
fi

repo_key="${GITHUB_REPOSITORY:-repository}"
repo_key="${repo_key//\//-}"
prefix="${DRYDOCK_INPUT_CACHE_KEY_PREFIX:-drydock}"
suffix="${DRYDOCK_INPUT_CACHE_KEY_SUFFIX:-v1}"
version="${DRYDOCK_RESOLVED_VERSION:-unknown}"
key_stem="${prefix}-${RUNNER_OS:-unknown}-${RUNNER_ARCH:-unknown}-${repo_key}"
key_base="${key_stem}-${version}-${suffix}"

cache_key="${DRYDOCK_INPUT_CACHE_KEY}"
if [[ -z "${cache_key}" ]]; then
  # actions/cache keys are immutable, so a static key is written once and then
  # frozen. Rotate the primary key per commit so the cache refreshes; the
  # restore-key prefixes below fall back to the most recent matching cache.
  cache_key="${key_base}"
  if [[ -n "${GITHUB_SHA:-}" ]]; then
    cache_key="${key_base}-${GITHUB_SHA}"
  fi
fi

restore_keys="${DRYDOCK_INPUT_CACHE_RESTORE_KEYS}"
if [[ -z "${restore_keys}" ]]; then
  restore_keys="${key_base}-
${key_stem}-${version}-
${key_stem}-
${prefix}-${RUNNER_OS:-unknown}-${RUNNER_ARCH:-unknown}-"
fi

diff_artifact_name="${DRYDOCK_INPUT_DIFF_ARTIFACT_NAME}"
if [[ -z "${diff_artifact_name}" ]]; then
  diff_artifact_name="drydock-diff-${GITHUB_RUN_ID:-run}-${GITHUB_RUN_ATTEMPT:-1}"
fi
diff_html_artifact_name="${DRYDOCK_INPUT_DIFF_HTML_ARTIFACT_NAME:-}"
if [[ -z "${diff_html_artifact_name}" ]]; then
  diff_html_artifact_name="drydock-diff-${GITHUB_RUN_ID:-run}-${GITHUB_RUN_ATTEMPT:-1}.html"
fi
image_artifact_name="${DRYDOCK_INPUT_IMAGE_ARTIFACT_NAME}"
if [[ -z "${image_artifact_name}" ]]; then
  image_artifact_name="drydock-images-${GITHUB_RUN_ID:-run}-${GITHUB_RUN_ATTEMPT:-1}"
fi

{
  echo "work-dir=${work_dir}"
  echo "cache-path=${cache_path}"
  echo "cache-key=${cache_key}"
  echo "cache-restore=${cache_restore}"
  echo "cache-save=${cache_save}"
  echo "trusted-context=${trusted_context}"
  echo "diff-artifact-name=${diff_artifact_name}"
  echo "diff-html-artifact-name=${diff_html_artifact_name}"
  echo "image-artifact-name=${image_artifact_name}"
  echo "restore-keys<<EOF"
  printf '%s\n' "${restore_keys}"
  echo "EOF"
} >> "${GITHUB_OUTPUT}"
