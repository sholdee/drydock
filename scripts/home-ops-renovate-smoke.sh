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

cleanup() {
  local status=$?
  local cleanup_status=0
  if [[ -n "${BASELINE}" && -d "${BASELINE}" ]]; then
    if ! git -C "${ROOT}" worktree remove --force "${BASELINE}" >&2; then
      echo "failed to remove baseline worktree: ${BASELINE}" >&2
      cleanup_status=1
    fi
  fi
  if [[ -n "${CURRENT}" && -d "${CURRENT}" ]]; then
    if ! git -C "${ROOT}" worktree remove --force "${CURRENT}" >&2; then
      echo "failed to remove current worktree: ${CURRENT}" >&2
      cleanup_status=1
    fi
  fi
  if [[ "${cleanup_status}" -eq 0 && -n "${TMP_DIR}" && -d "${TMP_DIR}" ]]; then
    rm -rf "${TMP_DIR}"
  fi
  if [[ "${cleanup_status}" -ne 0 ]]; then
    if [[ -n "${TMP_DIR}" ]]; then
      echo "leaving temporary smoke directory for inspection: ${TMP_DIR}" >&2
    fi
    if [[ "${status}" -eq 0 ]]; then
      exit 2
    fi
  fi
  exit "${status}"
}
trap cleanup EXIT

detect_renovate_chart_version() {
  local file="$1"
  local chart_name="${2:-renovate-operator}"

  awk -v chart_name="${chart_name}" '
    function indent(line) {
      match(line, /^[[:space:]]*/)
      return RLENGTH
    }
    function clean_value(line) {
      sub(/[[:space:]]*#.*/, "", line)
      sub(/^[[:space:]]*/, "", line)
      sub(/[[:space:]]*$/, "", line)
      gsub(/^"|"$/, "", line)
      return line
    }
    function reset_item() {
      in_item = 0
      item_name = ""
      item_version = ""
    }
    function read_inline_field(line, field) {
      sub("^[[:space:]]*-[[:space:]]*" field ":[[:space:]]*", "", line)
      return clean_value(line)
    }
    function read_child_field(line, field) {
      sub("^[[:space:]]*" field ":[[:space:]]*", "", line)
      return clean_value(line)
    }
    function finish_item() {
      if (!found && in_item && item_name == chart_name && item_version != "") {
        print item_version
        found = 1
      }
    }
    indent($0) == 0 && /^[[:space:]]*helmCharts:[[:space:]]*($|#)/ {
      in_helm = 1
      helm_indent = indent($0)
      chart_item_indent = -1
      reset_item()
      next
    }
    in_helm && $0 !~ /^[[:space:]]*($|#)/ && indent($0) <= helm_indent {
      finish_item()
      in_helm = 0
      reset_item()
    }
    !in_helm {
      next
    }
    chart_item_indent < 0 && indent($0) > helm_indent && /^[[:space:]]*-/ {
      chart_item_indent = indent($0)
    }
    chart_item_indent >= 0 && indent($0) == chart_item_indent && /^[[:space:]]*-/ {
      finish_item()
      if (found) {
        exit
      }
      reset_item()
      in_item = 1
      if ($0 ~ /^[[:space:]]*-[[:space:]]*name:[[:space:]]*/) {
        item_name = read_inline_field($0, "name")
      }
      if ($0 ~ /^[[:space:]]*-[[:space:]]*version:[[:space:]]*/) {
        item_version = read_inline_field($0, "version")
      }
      next
    }
    in_item && indent($0) == chart_item_indent + 2 && /^[[:space:]]*name:[[:space:]]*/ {
      item_name = read_child_field($0, "name")
      next
    }
    in_item && indent($0) == chart_item_indent + 2 && /^[[:space:]]*version:[[:space:]]*/ {
      item_version = read_child_field($0, "version")
      next
    }
    END {
      finish_item()
      if (!found) {
        exit 1
      }
    }
  ' "${file}"
}

update_renovate_chart_version() {
  local file="$1"
  local chart_name="$2"
  local version_to="$3"
  local output="$4"

  awk -v chart_name="${chart_name}" -v version_to="${version_to}" '
    function indent(line) {
      match(line, /^[[:space:]]*/)
      return RLENGTH
    }
    function clean_value(line) {
      sub(/[[:space:]]*#.*/, "", line)
      sub(/^[[:space:]]*/, "", line)
      sub(/[[:space:]]*$/, "", line)
      gsub(/^"|"$/, "", line)
      return line
    }
    function reset_item() {
      delete item_lines
      item_count = 0
      in_item = 0
      item_name = ""
      version_line = 0
    }
    function add_item_line(line) {
      item_count++
      item_lines[item_count] = line
    }
    function read_inline_field(line, field) {
      sub("^[[:space:]]*-[[:space:]]*" field ":[[:space:]]*", "", line)
      return clean_value(line)
    }
    function read_child_field(line, field) {
      sub("^[[:space:]]*" field ":[[:space:]]*", "", line)
      return clean_value(line)
    }
    function emit_item(   i, prefix) {
      if (in_item && item_name == chart_name && version_line > 0 && !changed) {
        prefix = item_lines[version_line]
        sub(/version:.*/, "version: ", prefix)
        item_lines[version_line] = prefix version_to
        changed = 1
      }
      for (i = 1; i <= item_count; i++) {
        print item_lines[i]
      }
      reset_item()
    }
    indent($0) == 0 && /^[[:space:]]*helmCharts:[[:space:]]*($|#)/ {
      in_helm = 1
      helm_indent = indent($0)
      chart_item_indent = -1
      reset_item()
      print
      next
    }
    in_helm && $0 !~ /^[[:space:]]*($|#)/ && indent($0) <= helm_indent {
      emit_item()
      in_helm = 0
      print
      next
    }
    in_helm && chart_item_indent < 0 && indent($0) > helm_indent && /^[[:space:]]*-/ {
      chart_item_indent = indent($0)
    }
    in_helm && chart_item_indent >= 0 && indent($0) == chart_item_indent && /^[[:space:]]*-/ {
      emit_item()
      in_item = 1
      add_item_line($0)
      if ($0 ~ /^[[:space:]]*-[[:space:]]*name:[[:space:]]*/) {
        item_name = read_inline_field($0, "name")
      }
      if ($0 ~ /^[[:space:]]*-[[:space:]]*version:[[:space:]]*/) {
        version_line = item_count
      }
      next
    }
    in_helm && chart_item_indent >= 0 && indent($0) == chart_item_indent + 2 && /^[[:space:]]*name:[[:space:]]*/ {
      add_item_line($0)
      item_name = read_child_field($0, "name")
      next
    }
    in_helm && chart_item_indent >= 0 && indent($0) == chart_item_indent + 2 && /^[[:space:]]*version:[[:space:]]*/ {
      add_item_line($0)
      version_line = item_count
      next
    }
    in_helm && in_item {
      add_item_line($0)
      next
    }
    { print }
    END {
      emit_item()
      if (!changed) {
        exit 1
      }
    }
  ' "${file}" > "${output}"
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
