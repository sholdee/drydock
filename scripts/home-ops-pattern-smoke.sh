#!/usr/bin/env bash
set -euo pipefail

HOME_OPS_ROOT="${HOME_OPS_ROOT:-/Users/ethan.shold/git/home-ops}"

ROOT="${HOME_OPS_ROOT}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_DIR=""
BASELINE=""
CURRENT=""
REMOTE_CACHE=""

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

detect_helm_chart_version() {
  local file="$1"
  local chart_name="$2"

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
      if (in_item && item_name == chart_name) {
        match_count++
        if (version_line > 0 && match_count == 1) {
          prefix = item_lines[version_line]
          sub(/version:.*/, "version: ", prefix)
          item_lines[version_line] = prefix version_to
          changed = 1
        }
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
      if (match_count != 1) {
        printf("expected exactly one top-level helmCharts entry named %s, found %d\n", chart_name, match_count) > "/dev/stderr"
        exit 1
      }
      if (!changed) {
        printf("chart %s did not have an editable top-level version field\n", chart_name) > "/dev/stderr"
        exit 1
      }
    }
  ' "${file}" > "${output}" || fail "failed to update chart ${chart_name} in ${file}"
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
