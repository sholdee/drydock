#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=setup-action/lib.sh
source "${script_dir}/lib.sh"

platform="$(detect_platform)"
repo="${DRYDOCK_RELEASE_REPOSITORY:-sholdee/drydock}"
version="${DRYDOCK_RESOLVED_VERSION:-${DRYDOCK_VERSION:-latest}}"
token="${DRYDOCK_GITHUB_TOKEN:-}"
expected_hash="${DRYDOCK_EXPECTED_HASH:-}"
cache_archive="${DRYDOCK_CACHE_ARCHIVE:-}"
cache_hit=false
cache_save=false

validate_repo "${repo}"

if [[ -z "${version}" || "${version}" == "latest" ]]; then
  version="$(resolve_latest "${repo}" "${token}")"
fi

version="$(normalize_version "${version}")"
asset_url="$(asset_url "${repo}" "${version}" "${platform}")"
checksums_url="$(checksums_url "${repo}" "${version}")"
asset_name="$(basename "${asset_url}")"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

headers=()
if [[ -n "${token}" ]]; then
  headers=(-H "Authorization: Bearer ${token}")
fi
install_dir="${DRYDOCK_INSTALL_DIR:-/usr/local/bin}"
if [[ -z "${install_dir}" ]]; then
  echo "setup-action requires a non-empty install-dir." >&2
  exit 1
fi

allow_unverified="${DRYDOCK_ALLOW_UNVERIFIED:-false}"
validate_bool allow-unverified "${allow_unverified}"

verify_checksum() {
  local selected_file="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum --check "${selected_file}"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "${selected_file}"
  else
    echo "No supported SHA-256 checksum tool found." >&2
    exit 1
  fi
}

verify_archive() {
  local expected="$1"
  local archive="$2"
  local selected_file="$3"

  printf '%s  %s\n' "${expected}" "${archive}" > "${selected_file}"
  verify_checksum "${selected_file}"
}

resolve_expected_hash() {
  if [[ -n "${expected_hash}" ]]; then
    printf '%s\n' "${expected_hash}"
    return 0
  fi

  checksum_http_code="$(
    curl --location --silent --show-error --write-out '%{http_code}' \
      "${headers[@]}" \
      --output "${tmpdir}/checksums.txt" \
      "${checksums_url}" || true
  )"

  if [[ "${checksum_http_code}" == "200" ]]; then
    if ! checksum_for_asset "${tmpdir}/checksums.txt" "${asset_name}"; then
      echo "checksums.txt did not contain the drydock artifact checksum." >&2
      exit 1
    fi
  elif [[ "${checksum_http_code}" == "404" ]]; then
    if [[ "${allow_unverified}" == "true" ]]; then
      echo "No checksums.txt artifact found; continuing because allow-unverified is true." >&2
      printf '\n'
    else
      echo "No checksums.txt artifact found; refusing unverified install." >&2
      exit 1
    fi
  else
    echo "Failed to download checksums.txt; HTTP status ${checksum_http_code}." >&2
    exit 1
  fi
}

archive_path=""
if [[ -n "${cache_archive}" && -s "${cache_archive}" ]]; then
  if [[ -n "${expected_hash}" ]]; then
    if verify_archive "${expected_hash}" "${cache_archive}" "${tmpdir}/checksums.cache.selected"; then
      archive_path="${cache_archive}"
      cache_hit=true
    else
      echo "Cached drydock archive failed checksum verification; downloading ${version}." >&2
    fi
  else
    echo "No expected checksum for cached drydock archive; downloading ${version}." >&2
  fi
fi

if [[ -z "${archive_path}" ]]; then
  archive_path="${tmpdir}/drydock.tar.gz"
  curl --fail --location --show-error "${headers[@]}" \
    --output "${archive_path}" \
    "${asset_url}"

  resolved_hash="$(resolve_expected_hash)"
  if [[ -n "${resolved_hash}" ]]; then
    verify_archive "${resolved_hash}" "${archive_path}" "${tmpdir}/checksums.download.selected"
    if [[ -n "${cache_archive}" ]]; then
      mkdir -p "$(dirname "${cache_archive}")"
      cp "${archive_path}" "${cache_archive}"
      cache_save=true
    fi
  fi
fi

mkdir -p "${install_dir}"
tar -xzf "${archive_path}" -C "${tmpdir}"
install -m 0755 "${tmpdir}/drydock" "${install_dir}/drydock"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${version}"
    echo "asset-url=${asset_url}"
    echo "install-dir=${install_dir}"
    echo "cache-hit=${cache_hit}"
    echo "cache-save=${cache_save}"
  } >> "${GITHUB_OUTPUT}"
fi
