#!/usr/bin/env bash

semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'

detect_platform() {
  case "${RUNNER_OS}-${RUNNER_ARCH}" in
    Linux-X64)
      printf 'linux-amd64\n'
      ;;
    Linux-ARM64)
      printf 'linux-arm64\n'
      ;;
    macOS-X64)
      printf 'darwin-amd64\n'
      ;;
    macOS-ARM64)
      printf 'darwin-arm64\n'
      ;;
    *)
      echo "Unsupported runner platform: ${RUNNER_OS}-${RUNNER_ARCH}" >&2
      return 1
      ;;
  esac
}

validate_repo() {
  local repo="$1"
  if [[ ! "${repo}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    echo "setup-action release-repository must be in owner/repo form, got '${repo}'." >&2
    return 1
  fi
}

validate_bool() {
  local name="$1"
  local value="$2"
  case "${value}" in
    true | false) ;;
    *)
      echo "setup-action ${name} must be true or false, got '${value}'." >&2
      return 1
      ;;
  esac
}

resolve_latest() {
  local repo="$1"
  local token="$2"

  if [[ -n "${token}" ]]; then
    curl --fail --silent --show-error --location \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/${repo}/releases/latest" \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
      | head -n 1
  else
    curl --write-out '%{url_effective}\n' --head --silent --show-error --location \
      "https://github.com/${repo}/releases/latest" \
      --output /dev/null \
      | sed 's|.*/||'
  fi
}

normalize_version() {
  local raw_version="$1"
  local candidate

  raw_version="${raw_version#refs/tags/}"
  candidate="${raw_version#v}"
  if [[ -z "${candidate}" || ! "${candidate}" =~ ${semver_re} ]]; then
    echo "setup-action requires latest or a semantic version tag, for example v0.1.7." >&2
    return 1
  fi
  printf 'v%s\n' "${candidate}"
}

asset_url() {
  local repo="$1"
  local version="$2"
  local platform="$3"

  printf 'https://github.com/%s/releases/download/%s/drydock_%s.tar.gz\n' "${repo}" "${version}" "${platform}"
}

checksums_url() {
  local repo="$1"
  local version="$2"

  printf 'https://github.com/%s/releases/download/%s/checksums.txt\n' "${repo}" "${version}"
}

sanitize_cache_part() {
  local value="$1"

  printf '%s' "${value}" | sed 's/[^A-Za-z0-9_.-]/-/g'
}

checksum_for_asset() {
  local checksums_file="$1"
  local asset_name="$2"

  awk -v asset="${asset_name}" '
    $2 == asset { print $1; found=1; exit }
    END { exit found ? 0 : 1 }
  ' "${checksums_file}"
}
