#!/usr/bin/env bash
set -euo pipefail

base_ref="${DRYDOCK_INPUT_BASE_REF:-}"
if [[ -z "${base_ref}" ]]; then
  base_ref="${DRYDOCK_PR_BASE_REF:-}"
fi
if [[ -z "${base_ref}" ]]; then
  echo "base-ref is required for drydock diff steps outside pull_request events." >&2
  exit 1
fi
if ! git check-ref-format --branch "${base_ref}" >/dev/null 2>&1; then
  echo "base-ref must be a valid branch name, got '${base_ref}'." >&2
  exit 1
fi

fetch_cmd=(git)
if [[ -n "${DRYDOCK_GITHUB_TOKEN:-}" ]]; then
  server_url="${GITHUB_SERVER_URL:-https://github.com}"
  existing_auth_header=false
  if git config --get-all "http.${server_url}/.extraheader" >/dev/null 2>&1; then
    existing_auth_header=true
  fi
  if [[ "${existing_auth_header}" == "false" ]]; then
    basic_auth="$(printf 'x-access-token:%s' "${DRYDOCK_GITHUB_TOKEN}" | base64 | tr -d '\n')"
    fetch_cmd+=(-c "http.${server_url}/.extraheader=AUTHORIZATION: basic ${basic_auth}")
  fi
fi
fetch_cmd+=(fetch --no-tags --depth=1 origin "${base_ref}:refs/remotes/origin/${base_ref}")
"${fetch_cmd[@]}"

{
  echo "base-ref=${base_ref}"
  echo "compare-ref=origin/${base_ref}"
} >> "${GITHUB_OUTPUT}"
