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

head_ref="${DRYDOCK_INPUT_HEAD_REF:-}"
if [[ -z "${head_ref}" ]]; then
  head_ref="HEAD"
fi

# git invocation prefix carrying optional one-shot auth, reused for every fetch.
git_cmd=(git)
if [[ -n "${DRYDOCK_GITHUB_TOKEN:-}" ]]; then
  server_url="${GITHUB_SERVER_URL:-https://github.com}"
  existing_auth_header=false
  if git config --get-all "http.${server_url}/.extraheader" >/dev/null 2>&1; then
    existing_auth_header=true
  fi
  if [[ "${existing_auth_header}" == "false" ]]; then
    basic_auth="$(printf 'x-access-token:%s' "${DRYDOCK_GITHUB_TOKEN}" | base64 | tr -d '\n')"
    git_cmd+=(-c "http.${server_url}/.extraheader=AUTHORIZATION: basic ${basic_auth}")
  fi
fi

# Force-update (leading '+') the remote-tracking ref, mirroring git's default
# clone refspec (+refs/heads/*:refs/remotes/origin/*). On a persistent runner a
# prior run can leave refs/remotes/origin/<base> at a stale, shallow tip; a
# depth-1 fetch then cannot prove a fast-forward and a non-forced refspec is
# rejected as non-fast-forward. A remote-tracking ref is always safe to force.
base_refspec="+${base_ref}:refs/remotes/origin/${base_ref}"

# Bring the base branch tip into the local repository.
"${git_cmd[@]}" fetch --no-tags --depth=1 origin "${base_refspec}"

# Record the base branch as origin's HEAD symref (no network: the tracking ref
# was just fetched). CI checkouts do not set origin/HEAD; drydock's diff
# self-repo gate reads it so sources pinned to the base branch NAME (e.g.
# targetRevision: main) resolve to the per-side trees instead of acquiring the
# remote. Non-fatal: without it drydock degrades to remote acquisition.
git remote set-head origin "${base_ref}" >/dev/null 2>&1 || true

repo_is_shallow() {
  local git_dir
  git_dir="$(git rev-parse --git-dir 2>/dev/null)" || return 1
  [[ -f "${git_dir}/shallow" ]]
}

resolve_merge_base() {
  git merge-base "origin/${base_ref}" "${head_ref}" 2>/dev/null
}

# Diff against the merge base of the base branch and the pull request head, not
# the base branch tip. A stale or diverged head would otherwise surface every
# change already merged into the base branch as a spurious difference. Deepen
# the shallow checkout (both base and head) until the common ancestor is in
# history, mirroring how GitHub computes the pull request diff.
#
# The base branch and the head are deepened with separate fetches on purpose: a
# single fetch that mixes a refspec and a bare commit only deepens the bare
# commit, leaving the named base ref at its tip and the merge base unreachable.
merge_base="$(resolve_merge_base || true)"
if [[ -z "${merge_base}" ]]; then
  head_sha="$(git rev-parse "${head_ref}" 2>/dev/null || true)"
  deepen=16
  deepen_cap=2048
  while [[ -z "${merge_base}" ]] && repo_is_shallow; do
    if [[ "${deepen}" -gt "${deepen_cap}" ]]; then
      "${git_cmd[@]}" fetch --no-tags --unshallow origin "${base_refspec}" >/dev/null 2>&1 || true
      if [[ -n "${head_sha}" ]]; then
        "${git_cmd[@]}" fetch --no-tags --deepen="${deepen}" origin "${head_sha}" >/dev/null 2>&1 || true
      fi
      merge_base="$(resolve_merge_base || true)"
      break
    fi
    "${git_cmd[@]}" fetch --no-tags --deepen="${deepen}" origin "${base_refspec}" >/dev/null 2>&1 || true
    if [[ -n "${head_sha}" ]]; then
      "${git_cmd[@]}" fetch --no-tags --deepen="${deepen}" origin "${head_sha}" >/dev/null 2>&1 || true
    fi
    merge_base="$(resolve_merge_base || true)"
    deepen=$((deepen * 2))
  done
fi

if [[ -n "${merge_base}" ]]; then
  compare_ref="${merge_base}"
else
  echo "warning: could not determine the merge base of '${base_ref}' and '${head_ref}'; diffing against the '${base_ref}' tip, which can surface changes already merged into the base branch." >&2
  compare_ref="origin/${base_ref}"
fi

{
  echo "base-ref=${base_ref}"
  echo "compare-ref=${compare_ref}"
} >> "${GITHUB_OUTPUT}"
