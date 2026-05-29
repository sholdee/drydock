#!/usr/bin/env bash
set -euo pipefail

case "${RUNNER_OS}-${RUNNER_ARCH}" in
  Linux-X64)
    platform="linux-amd64"
    ;;
  Linux-ARM64)
    platform="linux-arm64"
    ;;
  macOS-X64)
    platform="darwin-amd64"
    ;;
  macOS-ARM64)
    platform="darwin-arm64"
    ;;
  *)
    echo "Unsupported runner platform: ${RUNNER_OS}-${RUNNER_ARCH}" >&2
    exit 1
    ;;
esac

repo="${GITHUB_ACTION_REPOSITORY:-${GITHUB_REPOSITORY}}"
version="${DRYDOCK_VERSION:-latest}"
token="${DRYDOCK_GITHUB_TOKEN:-}"

resolve_latest() {
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

if [[ -z "${version}" || "${version}" == "latest" ]]; then
  version="$(resolve_latest)"
fi

version="${version#refs/tags/}"
candidate="${version#v}"
semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'
if [[ -z "${candidate}" || ! "${candidate}" =~ ${semver_re} ]]; then
  echo "setup-action requires latest or a semantic version tag, for example v0.1.7." >&2
  exit 1
fi
version="v${candidate}"

base_url="https://github.com/${repo}/releases/download/${version}"
asset_url="${base_url}/drydock_${platform}.tar.gz"
checksums_url="${base_url}/checksums.txt"

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
if [[ "${allow_unverified}" != "true" && "${allow_unverified}" != "false" ]]; then
  echo "setup-action allow-unverified must be true or false." >&2
  exit 1
fi

curl --fail --location --show-error "${headers[@]}" \
  --output "${tmpdir}/drydock.tar.gz" \
  "${asset_url}"
asset_name="$(basename "${asset_url}")"

verify_checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum --check "${tmpdir}/checksums.selected"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "${tmpdir}/checksums.selected"
  else
    echo "No supported SHA-256 checksum tool found." >&2
    exit 1
  fi
}

checksum_http_code="$(
  curl --location --silent --show-error --write-out '%{http_code}' \
    "${headers[@]}" \
    --output "${tmpdir}/checksums.txt" \
    "${checksums_url}" || true
)"

if [[ "${checksum_http_code}" == "200" ]]; then
  expected_line="$(
    awk -v asset="${asset_name}" '
      $2 == asset { print; found=1; exit }
      END { exit found ? 0 : 1 }
    ' "${tmpdir}/checksums.txt" || true
  )"
  if [[ -z "${expected_line}" ]]; then
    echo "checksums.txt did not contain the drydock artifact checksum." >&2
    exit 1
  fi
  expected_hash="${expected_line%% *}"
  printf '%s  %s\n' "${expected_hash}" "${tmpdir}/drydock.tar.gz" > "${tmpdir}/checksums.selected"
  verify_checksum
elif [[ "${checksum_http_code}" == "404" ]]; then
  if [[ "${allow_unverified}" == "true" ]]; then
    echo "No checksums.txt artifact found; continuing because allow-unverified is true." >&2
  else
    echo "No checksums.txt artifact found; refusing unverified install." >&2
    exit 1
  fi
else
  echo "Failed to download checksums.txt; HTTP status ${checksum_http_code}." >&2
  exit 1
fi

mkdir -p "${install_dir}"
tar -xzf "${tmpdir}/drydock.tar.gz" -C "${tmpdir}"
install -m 0755 "${tmpdir}/drydock" "${install_dir}/drydock"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${version}"
    echo "asset-url=${asset_url}"
    echo "install-dir=${install_dir}"
  } >> "${GITHUB_OUTPUT}"
fi
