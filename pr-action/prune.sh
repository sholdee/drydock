#!/usr/bin/env bash
set -euo pipefail

drydock_bin="${DRYDOCK_BIN:-drydock}"
cache_path="${DRYDOCK_CACHE_PATH}"
max_size="${DRYDOCK_INPUT_CACHE_PRUNE_MAX_SIZE}"
path="${DRYDOCK_INPUT_PATH:-.}"

if ! command -v "${drydock_bin}" > /dev/null 2>&1; then
  echo "::notice::drydock: prune skipped — drydock binary not found at '${drydock_bin}'"
  exit 0
fi

stderr_tmp="$(mktemp)"
prune_exit=0
prune_output="$(
  "${drydock_bin}" cache prune \
    --max-size "${max_size}" \
    --yes \
    --path "${path}" \
    --git-cache-dir "${cache_path}/git" \
    --chart-cache-dir "${cache_path}/charts" \
    --remote-cache-dir "${cache_path}/remotes" \
    --render-cache-dir "${cache_path}/renders" \
    -o json \
    2>"${stderr_tmp}"
)" || prune_exit=$?
prune_err="$(cat "${stderr_tmp}")"
rm -f "${stderr_tmp}"

if [[ "${prune_exit}" -ne 0 ]]; then
  if echo "${prune_err}" | grep -q "unknown flag: --max-size"; then
    echo "::notice::drydock: prune skipped — installed drydock binary does not support cache prune --max-size; upgrade to a release that includes this flag"
    exit 0
  fi
  # Collapse newlines so drydock stderr cannot smuggle extra lines into the
  # runner log where they would be re-scanned for workflow commands.
  echo "::warning::drydock: cache prune failed (exit ${prune_exit}): ${prune_err//$'\n'/ }"
  exit 0
fi

# Log a one-line summary from the JSON output; jq may be absent, so this is
# best-effort — absence of jq is not an error.
if command -v jq > /dev/null 2>&1; then
  removed="$(echo "${prune_output}" | jq -r '.removedCount // 0' 2>/dev/null || echo '?')"
  freed="$(echo "${prune_output}" | jq -r '.sizeEvictedBytes // 0' 2>/dev/null || echo '?')"
  remaining="$(echo "${prune_output}" | jq -r '.totalSizeBytes // 0' 2>/dev/null || echo '?')"
  echo "drydock: cache prune complete — removed ${removed} entries, freed ${freed} bytes by size, ${remaining} bytes remain"
else
  echo "drydock: cache prune complete"
fi
