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

optional_int() {
  case "$1" in
    '' ) return 0 ;;
    *[!0-9]* )
      echo "$2 must be an integer, got '$1'." >&2
      exit 1
      ;;
  esac
}

positive_int() {
  optional_int "$1" "$2"
  if [[ -n "$1" && "$1" == "0" ]]; then
    echo "$2 must be greater than zero." >&2
    exit 1
  fi
}

append_bool_flag() {
  # shellcheck disable=SC2178
  local -n target="$1"
  local value="$2"
  local flag="$3"
  if [[ "${value}" == "true" ]]; then
    target+=("${flag}")
  fi
}

append_value_flag() {
  # shellcheck disable=SC2178
  local -n target="$1"
  local value="$2"
  local flag="$3"
  if [[ -n "${value}" ]]; then
    target+=("${flag}" "${value}")
  fi
}

append_lines() {
  # shellcheck disable=SC2178
  local -n target="$1"
  local value="$2"
  local flag="$3"
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    target+=("${flag}" "${line}")
  done <<< "${value}"
}

append_extra_lines() {
  # shellcheck disable=SC2178
  local -n target="$1"
  local value="$2"
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    target+=("${line}")
  done <<< "${value}"
}

extract_image_diff_json() {
  local json_file="$1"
  local added_file="$2"
  local count_file="$3"

  if ! command -v jq >/dev/null 2>&1; then
    echo "drydock diff images -o name is not supported by this drydock binary, and jq is required for JSON fallback parsing." >&2
    return 2
  fi

  jq -r '(.added // [])[]' "${json_file}" > "${added_file}"
  jq -r '((.added // []) | length) + ((.removed // []) | length)' "${json_file}" > "${count_file}"
}

workflow_run_url() {
  local server_url="${GITHUB_SERVER_URL:-}"
  local repository="${GITHUB_REPOSITORY:-}"
  local run_id="${GITHUB_RUN_ID:-}"

  if [[ -z "${server_url}" || -z "${repository}" || -z "${run_id}" ]]; then
    return 0
  fi

  printf '%s/%s/actions/runs/%s\n' "${server_url%/}" "${repository}" "${run_id}"
}

capture_command() {
  local stdout_file="$1"
  local stderr_file="$2"
  shift 2

  set +e
  "$@" > "${stdout_file}" 2> "${stderr_file}"
  local status=$?
  set -e

  if [[ -s "${stdout_file}" ]]; then
    cat "${stdout_file}"
  fi
  if [[ -s "${stderr_file}" ]]; then
    cat "${stderr_file}" >&2
  fi
  return "${status}"
}

capture_command_quiet() {
  local stdout_file="$1"
  local stderr_file="$2"
  shift 2

  "$@" > "${stdout_file}" 2> "${stderr_file}"
}

diff_comment_budget() {
  local requested="$1"
  local cap=60000
  local reserve=512
  local minimum=1024
  local effective

  effective="$((10#${requested}))"
  if [[ "${effective}" -gt "${cap}" ]]; then
    effective="${cap}"
  fi
  if [[ "${effective}" -lt $((minimum + reserve)) ]]; then
    effective=$((minimum + reserve))
  fi
  printf '%s\n' "$((effective - reserve))"
}

append_diff_artifact_footer() {
  local comment_file="$1"
  local max_bytes=60000
  local footer
  footer="- Full diff output: [${DRYDOCK_DIFF_ARTIFACT_NAME} artifact](${run_url})."
  local current footer_bytes
  current="$(wc -c < "${comment_file}")"
  footer_bytes="$(printf '\n%s\n' "${footer}" | wc -c)"
  if [[ $((current + footer_bytes)) -le "${max_bytes}" ]]; then
    {
      echo
      echo "${footer}"
    } >> "${comment_file}"
  fi
}

validate_diff_comment_size() {
  local comment_file="$1"
  local max_bytes=60000
  if [[ "$(wc -c < "${comment_file}")" -gt "${max_bytes}" ]]; then
    echo "drydock diff comment exceeds ${max_bytes} bytes." >&2
    exit 1
  fi
}

bool "${DRYDOCK_INPUT_RUN_TEST}" run-test
bool "${DRYDOCK_INPUT_RUN_DIFF}" run-diff
bool "${DRYDOCK_INPUT_RUN_IMAGE_DIFF}" run-image-diff
bool "${DRYDOCK_INPUT_SKIP_SECRETS}" skip-secrets
bool "${DRYDOCK_INPUT_OFFLINE}" offline
bool "${DRYDOCK_INPUT_STRICT}" strict
bool "${DRYDOCK_INPUT_STRICT_CHANGED_ONLY}" strict-changed-only
bool "${DRYDOCK_INPUT_SHOW_IGNORED_FIELDS}" show-ignored-fields
bool "${DRYDOCK_INPUT_ENABLE_AVP_COMPAT}" enable-avp-compat
bool "${DRYDOCK_INPUT_ENABLE_PLUGINS}" enable-plugins
bool "${DRYDOCK_INPUT_DISABLE_PLUGIN_POLICY}" disable-plugin-policy
bool "${DRYDOCK_INPUT_FAIL_ON_RENDER_ERROR}" fail-on-render-error
bool "${DRYDOCK_INPUT_FAIL_ON_DIFF}" fail-on-diff
bool "${DRYDOCK_INPUT_FAIL_ON_IMAGE_DIFF}" fail-on-image-diff
bool "${DRYDOCK_INPUT_COMMENT_EMPTY}" comment-empty
bool "${DRYDOCK_INPUT_UPLOAD_ARTIFACTS}" upload-artifacts
positive_int "${DRYDOCK_INPUT_DIFF_MAX_BYTES}" diff-max-bytes
optional_int "${DRYDOCK_INPUT_PARALLELISM}" parallelism
optional_int "${DRYDOCK_INPUT_MAX_DISCOVERY_DEPTH}" max-discovery-depth

case "${DRYDOCK_INPUT_COMMENT_MODE}" in
  none | diff | images | both) ;;
  *)
    echo "comment-mode must be none, diff, images, or both, got '${DRYDOCK_INPUT_COMMENT_MODE}'." >&2
    exit 1
    ;;
esac
case "${DRYDOCK_INPUT_CHANGED_ONLY}" in
  '' | true | false) ;;
  *)
    echo "changed-only must be empty, true, or false, got '${DRYDOCK_INPUT_CHANGED_ONLY}'." >&2
    exit 1
    ;;
esac

work_dir="${DRYDOCK_ACTION_WORK_DIR}"
mkdir -p "${work_dir}"
cache_path="${DRYDOCK_CACHE_PATH:-}"
if [[ -n "${cache_path}" ]]; then
  mkdir -p "${cache_path}/git" "${cache_path}/charts" "${cache_path}/remotes"
fi

test_stdout="${work_dir}/test.out"
test_stderr="${work_dir}/test.err"
diff_path="${work_dir}/diff.txt"
diff_stderr="${work_dir}/diff.err"
images_path="${work_dir}/added-images.txt"
images_json_path="${work_dir}/images.json"
images_count_path="${work_dir}/images.count"
images_stderr="${work_dir}/images.err"
diff_comment_path="${work_dir}/diff-comment.md"
images_comment_path="${work_dir}/images-comment.md"

path="${DRYDOCK_INPUT_PATH:-.}"
repo="${DRYDOCK_INPUT_REPO:-.}"
head_ref="${DRYDOCK_INPUT_HEAD_REF:-HEAD}"
base_compare_ref="${DRYDOCK_BASE_COMPARE_REF:-}"
drydock_bin="${DRYDOCK_BIN:-drydock}"
run_url="$(workflow_run_url)"

common_args=()
if [[ -n "${cache_path}" ]]; then
  common_args+=(
    "--git-cache-dir" "${cache_path}/git"
    "--chart-cache-dir" "${cache_path}/charts"
    "--remote-cache-dir" "${cache_path}/remotes"
  )
fi
append_bool_flag common_args "${DRYDOCK_INPUT_SKIP_SECRETS}" "--skip-secrets"
append_bool_flag common_args "${DRYDOCK_INPUT_OFFLINE}" "--offline"
append_bool_flag common_args "${DRYDOCK_INPUT_STRICT}" "--strict"
append_bool_flag common_args "${DRYDOCK_INPUT_STRICT_CHANGED_ONLY}" "--strict-changed-only"
append_bool_flag common_args "${DRYDOCK_INPUT_ENABLE_AVP_COMPAT}" "--enable-avp-compat"
append_bool_flag common_args "${DRYDOCK_INPUT_ENABLE_PLUGINS}" "--enable-plugins"
append_bool_flag common_args "${DRYDOCK_INPUT_DISABLE_PLUGIN_POLICY}" "--disable-plugin-policy"
append_value_flag common_args "${DRYDOCK_INPUT_PARALLELISM}" "--parallelism"
append_value_flag common_args "${DRYDOCK_INPUT_MAX_DISCOVERY_DEPTH}" "--max-discovery-depth"
append_value_flag common_args "${DRYDOCK_INPUT_PLUGIN_POLICY_PATH}" "--plugin-policy-path"
append_value_flag common_args "${DRYDOCK_INPUT_PLUGIN_POLICY_REF}" "--plugin-policy-ref"
append_value_flag common_args "${DRYDOCK_INPUT_PLUGIN_POLICY_REPO}" "--plugin-policy-repo"
append_lines common_args "${DRYDOCK_INPUT_DISCOVER_KUSTOMIZE}" "--discover-kustomize"
append_lines common_args "${DRYDOCK_INPUT_REPO_MAP}" "--repo-map"
if [[ -n "${DRYDOCK_INPUT_CHANGED_ONLY}" ]]; then
  common_args+=("--changed-only=${DRYDOCK_INPUT_CHANGED_ONLY}")
fi

render_status="skipped"
has_diff="false"
has_images="false"
has_image_diff="false"
failed="false"

if [[ "${DRYDOCK_INPUT_RUN_TEST}" == "true" ]]; then
  test_args=("${drydock_bin}" test apps --path "${path}" "${common_args[@]}")
  append_extra_lines test_args "${DRYDOCK_INPUT_EXTRA_TEST_ARGS}"
  if capture_command "${test_stdout}" "${test_stderr}" "${test_args[@]}"; then
    render_status="passed"
  else
    render_status="failed"
    if [[ "${DRYDOCK_INPUT_FAIL_ON_RENDER_ERROR}" == "true" ]]; then
      failed="true"
    fi
  fi
fi

if [[ "${DRYDOCK_INPUT_RUN_DIFF}" == "true" ]]; then
  if [[ -z "${base_compare_ref}" ]]; then
    echo "A base ref is required when run-diff is true." >&2
    exit 1
  fi
  diff_args=("${drydock_bin}" diff apps --repo "${repo}" --ref "${head_ref}" --ref-orig "${base_compare_ref}" "${common_args[@]}")
  append_bool_flag diff_args "${DRYDOCK_INPUT_SHOW_IGNORED_FIELDS}" "--show-ignored-fields"
  append_extra_lines diff_args "${DRYDOCK_INPUT_EXTRA_DIFF_ARGS}"
  diff_args+=(
    -o markdown
    --markdown-max-bytes "$(diff_comment_budget "${DRYDOCK_INPUT_DIFF_MAX_BYTES}")"
    --raw-output-file "${diff_path}"
    --exit-code=false
  )
  if ! capture_command "${diff_comment_path}" "${diff_stderr}" "${diff_args[@]}"; then
    failed="true"
  fi
  if [[ -s "${diff_path}" ]]; then
    has_diff="true"
    if [[ "${DRYDOCK_INPUT_FAIL_ON_DIFF}" == "true" ]]; then
      failed="true"
    fi
  fi
else
  : > "${diff_path}"
  : > "${diff_comment_path}"
fi

if [[ "${DRYDOCK_INPUT_RUN_IMAGE_DIFF}" == "true" ]]; then
  if [[ -z "${base_compare_ref}" ]]; then
    echo "A base ref is required when run-image-diff is true." >&2
    exit 1
  fi
  image_args=("${drydock_bin}" diff images --repo "${repo}" --ref "${head_ref}" --ref-orig "${base_compare_ref}" -o name "${common_args[@]}")
  append_extra_lines image_args "${DRYDOCK_INPUT_EXTRA_IMAGE_DIFF_ARGS}"
  if capture_command_quiet "${images_path}" "${images_stderr}" "${image_args[@]}"; then
    image_status=0
  else
    image_status=$?
  fi
  if [[ "${image_status}" -eq 2 ]] && grep -q "name output is not supported for diff images" "${images_stderr}"; then
    image_args=("${drydock_bin}" diff images --repo "${repo}" --ref "${head_ref}" --ref-orig "${base_compare_ref}" -o json --exit-code=false "${common_args[@]}")
    append_extra_lines image_args "${DRYDOCK_INPUT_EXTRA_IMAGE_DIFF_ARGS}"
    if capture_command_quiet "${images_json_path}" "${images_stderr}" "${image_args[@]}"; then
      image_status=0
    else
      image_status=$?
    fi
    if [[ "${image_status}" -eq 0 ]]; then
      extract_image_diff_json "${images_json_path}" "${images_path}" "${images_count_path}"
      if [[ "$(cat "${images_count_path}")" -gt 0 ]]; then
        image_status=1
      fi
    fi
  fi
  if [[ "${image_status}" -ne 0 && "${image_status}" -ne 1 ]]; then
    if [[ -s "${images_path}" ]]; then
      cat "${images_path}"
    fi
    if [[ -s "${images_stderr}" ]]; then
      cat "${images_stderr}" >&2
    fi
  fi
  case "${image_status}" in
    0) ;;
    1)
      has_image_diff="true"
      if [[ "${DRYDOCK_INPUT_FAIL_ON_IMAGE_DIFF}" == "true" ]]; then
        failed="true"
      fi
      ;;
    *)
      failed="true"
      ;;
  esac
  if [[ -s "${images_path}" ]]; then
    has_images="true"
    has_image_diff="true"
  fi
else
  : > "${images_path}"
fi

comment_mode="${DRYDOCK_INPUT_COMMENT_MODE}"
diff_comment="false"
images_comment="false"
if [[ "${comment_mode}" == "diff" || "${comment_mode}" == "both" ]]; then
  if [[ "${has_diff}" == "true" || "${DRYDOCK_INPUT_COMMENT_EMPTY}" == "true" ]]; then
    diff_comment="true"
  fi
fi
if [[ "${comment_mode}" == "images" || "${comment_mode}" == "both" ]]; then
  if [[ "${has_images}" == "true" || "${DRYDOCK_INPUT_COMMENT_EMPTY}" == "true" ]]; then
    images_comment="true"
  fi
fi

if [[ "${diff_comment}" == "true" ]]; then
  if [[ ! -s "${diff_comment_path}" ]]; then
    {
      echo "## drydock desired state diff"
      echo
      echo "**Summary:** 0 apps, 0 resources, +0/-0."
      echo
      echo "No rendered manifest differences detected."
    } > "${diff_comment_path}"
  fi
  if [[ "${has_diff}" == "true" && "${DRYDOCK_INPUT_UPLOAD_ARTIFACTS}" == "true" && -n "${run_url}" ]]; then
    append_diff_artifact_footer "${diff_comment_path}"
  fi
  validate_diff_comment_size "${diff_comment_path}"
fi

if [[ "${images_comment}" == "true" ]]; then
  {
    echo "### drydock added images"
    echo
    if [[ "${has_images}" == "true" ]]; then
      while IFS= read -r image; do
        [[ -n "${image}" ]] || continue
        printf -- "- \`%s\`\n" "${image}"
      done < "${images_path}"
      if [[ "${DRYDOCK_INPUT_UPLOAD_ARTIFACTS}" == "true" && "${diff_comment}" != "true" && -n "${run_url}" ]]; then
        echo
        echo "- Full added image output: [${DRYDOCK_IMAGE_ARTIFACT_NAME} artifact](${run_url})."
      fi
    else
      echo "No added rendered images detected."
    fi
  } > "${images_comment_path}"
fi

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "## drydock"
    echo
    echo "| Check | Result |"
    echo "| --- | --- |"
    echo "| Render test | ${render_status} |"
    echo "| Manifest diff | ${has_diff} |"
    echo "| Image additions | ${has_images} |"
    echo "| Any image diff | ${has_image_diff} |"
    echo
    echo "- Diff artifact: ${DRYDOCK_DIFF_ARTIFACT_NAME}"
    echo "- Image artifact: ${DRYDOCK_IMAGE_ARTIFACT_NAME}"
  } >> "${GITHUB_STEP_SUMMARY}"
fi

{
  echo "has-diff=${has_diff}"
  echo "has-images=${has_images}"
  echo "has-image-diff=${has_image_diff}"
  echo "render-status=${render_status}"
  echo "diff-path=${diff_path}"
  echo "images-path=${images_path}"
  echo "diff-comment-path=${diff_comment_path}"
  echo "images-comment-path=${images_comment_path}"
  echo "diff-comment=${diff_comment}"
  echo "images-comment=${images_comment}"
  echo "diff-artifact-name=${DRYDOCK_DIFF_ARTIFACT_NAME}"
  echo "image-artifact-name=${DRYDOCK_IMAGE_ARTIFACT_NAME}"
} >> "${GITHUB_OUTPUT}"

if [[ "${failed}" == "true" ]]; then
  exit 1
fi
