# shellcheck shell=bash
# Shared helpers for the home-ops smoke scripts.
#
# Contract: source this file after defining the ROOT, TMP_DIR, BASELINE, and
# CURRENT globals, then register the exit handler with: trap cleanup EXIT

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

# update_helm_chart_version FILE CHART_NAME VERSION_TO OUTPUT [STRICT]
#
# Rewrites the version of the first top-level helmCharts entry named
# CHART_NAME into OUTPUT. With STRICT=1, additionally fails loudly unless
# exactly one matching entry with an editable version field exists.
update_helm_chart_version() {
  local file="$1"
  local chart_name="$2"
  local version_to="$3"
  local output="$4"
  local strict="${5:-0}"

  awk -v chart_name="${chart_name}" -v version_to="${version_to}" -v strict="${strict}" '
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
        if (version_line > 0 && !changed && (!strict || match_count == 1)) {
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
      if (strict && match_count != 1) {
        printf("expected exactly one top-level helmCharts entry named %s, found %d\n", chart_name, match_count) > "/dev/stderr"
        exit 1
      }
      if (!changed) {
        if (strict) {
          printf("chart %s did not have an editable top-level version field\n", chart_name) > "/dev/stderr"
        }
        exit 1
      }
    }
  ' "${file}" > "${output}"
}
