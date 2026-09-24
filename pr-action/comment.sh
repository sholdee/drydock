#!/usr/bin/env bash
set -euo pipefail

drydock_bin="${DRYDOCK_BIN:-drydock}"
message_id="${DRYDOCK_COMMENT_MESSAGE_ID}"
comment_path="${DRYDOCK_COMMENT_PATH}"
pr_number="${DRYDOCK_PR_NUMBER}"

# Capability probe: older drydock releases have no `pr comment`. Fail loudly
# instead of silently skipping the comment — `comment-continue-on-error`
# (default true) keeps this from failing the job unless the user opted in.
if ! "${drydock_bin}" pr comment --help > /dev/null 2>&1; then
  echo "${drydock_bin} has no 'pr comment' subcommand; pr-action needs a drydock release that includes it — unpin or raise the action's 'version' input" >&2
  exit 1
fi

# The token travels in the environment only: on argv it would land in the
# runner's process list and in command traces.
export DRYDOCK_GITHUB_TOKEN="${DRYDOCK_GITHUB_TOKEN:-}"

"${drydock_bin}" pr comment \
  --repository "${GITHUB_REPOSITORY}" \
  --number "${pr_number}" \
  --message-id "${message_id}" \
  --body-file "${comment_path}"
