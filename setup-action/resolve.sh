#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=setup-action/lib.sh
source "${script_dir}/lib.sh"

platform="$(detect_platform)"
repo="${DRYDOCK_RELEASE_REPOSITORY:-sholdee/drydock}"
version="${DRYDOCK_VERSION:-latest}"
token="${DRYDOCK_GITHUB_TOKEN:-}"
install_dir="${DRYDOCK_INSTALL_DIR:-/usr/local/bin}"
allow_unverified="${DRYDOCK_ALLOW_UNVERIFIED:-false}"
cache_binary="${DRYDOCK_CACHE_BINARY:-true}"
cache_suffix="${DRYDOCK_CACHE_BINARY_KEY_SUFFIX:-v1}"

validate_repo "${repo}"
validate_bool allow-unverified "${allow_unverified}"
validate_bool cache-binary "${cache_binary}"

if [[ -z "${install_dir}" ]]; then
  echo "setup-action requires a non-empty install-dir." >&2
  exit 1
fi

if [[ -z "${version}" || "${version}" == "latest" ]]; then
  version="$(resolve_latest "${repo}" "${token}")"
fi
version="$(normalize_version "${version}")"

asset_url_value="$(asset_url "${repo}" "${version}" "${platform}")"
checksums_url_value="$(checksums_url "${repo}" "${version}")"
asset_name="$(basename "${asset_url_value}")"
cache_enabled=false
expected_hash=""

if [[ "${cache_binary}" == "true" && "${allow_unverified}" == "false" ]]; then
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "${tmpdir}"' EXIT

  headers=()
  if [[ -n "${token}" ]]; then
    headers=(-H "Authorization: Bearer ${token}")
  fi

  if curl --fail --silent --show-error --location \
    "${headers[@]}" \
    --output "${tmpdir}/checksums.txt" \
    "${checksums_url_value}"; then
    if expected_hash="$(checksum_for_asset "${tmpdir}/checksums.txt" "${asset_name}")"; then
      cache_enabled=true
    else
      echo "checksums.txt did not contain ${asset_name}; skipping drydock binary cache." >&2
    fi
  else
    echo "Could not resolve drydock checksum; skipping drydock binary cache." >&2
  fi
fi

runner_temp="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
repo_key="$(sanitize_cache_part "${repo}")"
version_key="$(sanitize_cache_part "${version}")"
platform_key="$(sanitize_cache_part "${platform}")"
suffix_key="$(sanitize_cache_part "${cache_suffix}")"
hash_key="$(sanitize_cache_part "${expected_hash}")"

cache_path="${runner_temp%/}/drydock-binary-cache/${repo_key}/${version_key}/${platform_key}/${hash_key}"
cache_key="drydock-binary-${RUNNER_OS:-unknown}-${RUNNER_ARCH:-unknown}-${repo_key}-${version_key}-${platform_key}-${hash_key}-${suffix_key}"
cache_archive="${cache_path}/drydock.tar.gz"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${version}"
    echo "asset-url=${asset_url_value}"
    echo "checksums-url=${checksums_url_value}"
    echo "install-dir=${install_dir}"
    echo "cache-enabled=${cache_enabled}"
    echo "cache-key=${cache_key}"
    echo "cache-path=${cache_path}"
    echo "cache-archive=${cache_archive}"
    echo "expected-hash=${expected_hash}"
  } >> "${GITHUB_OUTPUT}"
fi
