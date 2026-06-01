#!/usr/bin/env bash
set -Eeuo pipefail

REPO="sholdee/drydock"
PROJECT="drydock"
DEFAULT_TARGET="/usr/local/bin/drydock"
COSIGN_ISSUER="https://token.actions.githubusercontent.com"
COSIGN_IDENTITY="https://github.com/sholdee/drydock/.github/workflows/release.yml@refs/heads/main"

ARCHIVE=""
AUTO_YES=false
BASH_COMPLETION_DIR=""
CHECKSUM_STATUS="not checked"
COSIGN_STATUS="not checked"
DRY_RUN=false
EXTRACTED_BINARY=""
FISH_COMPLETION_DIR=""
IF_OUTDATED=false
NO_COMPLETIONS=false
REQUIRE_COSIGN=false
TARGET=""
TARGET_PATH=""
VERSION=""
WORKDIR=""
ZSH_COMPLETION_DIR=""

usage() {
  cat <<'EOF'
Install or update the drydock standalone binary.

Usage:
  install-drydock.sh [options]

Options:
  --version <tag>               Install a specific release tag instead of latest
  --target <path>               Install to a specific binary path
  --dry-run                     Download and verify only; do not install
  --if-outdated                 Exit without changes when the installed binary matches the selected release
  --require-cosign              Fail if cosign verification cannot be completed
  --no-completions              Do not install shell completions
  --bash-completion-dir <path>  Install bash completion as <path>/drydock
  --zsh-completion-dir <path>   Install zsh completion as <path>/_drydock
  --fish-completion-dir <path>  Install fish completion as <path>/drydock.fish
  -y, --yes                     Accept prompts and run non-interactively
  -h, --help                    Show this help

Examples:
  ./install-drydock.sh --yes
  ./install-drydock.sh --version v0.1.0 --yes
  ./install-drydock.sh --target "$HOME/bin/drydock" --yes
EOF
}

log() { printf "%s\n" "$*"; }
warn() { printf "warning: %s\n" "$*" >&2; }
err() { printf "error: %s\n" "$*" >&2; }

install_testing_enabled() {
  [[ "${DRYDOCK_INSTALL_TESTING:-}" == "1" ]]
}

cleanup() {
  if [[ -n "${WORKDIR}" && -d "${WORKDIR}" ]]; then
    rm -rf "${WORKDIR}"
  fi
}

trap cleanup EXIT

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        VERSION="${2:-}"
        [[ -n "${VERSION}" ]] || { err "--version requires a tag"; exit 1; }
        shift 2
        ;;
      --target)
        TARGET="${2:-}"
        [[ -n "${TARGET}" ]] || { err "--target requires a path"; exit 1; }
        shift 2
        ;;
      --dry-run)
        DRY_RUN=true
        shift
        ;;
      --if-outdated)
        IF_OUTDATED=true
        shift
        ;;
      --require-cosign)
        REQUIRE_COSIGN=true
        shift
        ;;
      --no-completions)
        NO_COMPLETIONS=true
        shift
        ;;
      --bash-completion-dir)
        BASH_COMPLETION_DIR="${2:-}"
        [[ -n "${BASH_COMPLETION_DIR}" ]] || { err "--bash-completion-dir requires a path"; exit 1; }
        shift 2
        ;;
      --zsh-completion-dir)
        ZSH_COMPLETION_DIR="${2:-}"
        [[ -n "${ZSH_COMPLETION_DIR}" ]] || { err "--zsh-completion-dir requires a path"; exit 1; }
        shift 2
        ;;
      --fish-completion-dir)
        FISH_COMPLETION_DIR="${2:-}"
        [[ -n "${FISH_COMPLETION_DIR}" ]] || { err "--fish-completion-dir requires a path"; exit 1; }
        shift 2
        ;;
      -y|--yes)
        AUTO_YES=true
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        err "unknown argument: $1"
        usage >&2
        exit 1
        ;;
    esac
  done
}

detect_archive() {
  local os raw_arch arch detected_os

  detected_os="$(uname -s)"
  if install_testing_enabled && [[ -n "${DRYDOCK_INSTALL_TEST_OS:-}" ]]; then
    detected_os="${DRYDOCK_INSTALL_TEST_OS}"
  fi

  case "${detected_os}" in
    Linux|linux) os="linux" ;;
    Darwin|darwin) os="darwin" ;;
    *)
      err "unsupported OS: ${detected_os}. Supported OSes: Linux, Darwin."
      exit 1
      ;;
  esac

  raw_arch="$(uname -m)"
  if install_testing_enabled && [[ -n "${DRYDOCK_INSTALL_TEST_ARCH:-}" ]]; then
    raw_arch="${DRYDOCK_INSTALL_TEST_ARCH}"
  fi
  case "${raw_arch}" in
    amd64|x86_64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)
      err "unsupported architecture: ${raw_arch}. Supported architectures: amd64, arm64."
      exit 1
      ;;
  esac

  ARCHIVE="${PROJECT}_${os}-${arch}.tar.gz"
}

release_base_url() {
  if [[ -n "${VERSION}" ]]; then
    printf "https://github.com/%s/releases/download/%s" "${REPO}" "${VERSION}"
  else
    printf "https://github.com/%s/releases/latest/download" "${REPO}"
  fi
}

release_label() {
  if [[ -n "${VERSION}" ]]; then
    printf "%s" "${VERSION}"
  else
    printf "latest"
  fi
}

ensure_workdir() {
  if [[ -z "${WORKDIR}" ]]; then
    WORKDIR="$(mktemp -d)"
  fi
}

download_assets() {
  local base_url

  ensure_workdir
  base_url="$(release_base_url)"

  log "Downloading ${ARCHIVE} from $(release_label)"
  curl -fsSL "${base_url}/${ARCHIVE}" -o "${WORKDIR}/${ARCHIVE}"
  log "Downloading checksums.txt"
  curl -fsSL "${base_url}/checksums.txt" -o "${WORKDIR}/checksums.txt"
  if curl -fsSL "${base_url}/${ARCHIVE}.sigstore.json" -o "${WORKDIR}/${ARCHIVE}.sigstore.json"; then
    :
  else
    rm -f "${WORKDIR:?}/${ARCHIVE}.sigstore.json"
  fi
}

hash_file() {
  local path="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
  else
    shasum -a 256 "${path}" | awk '{print $1}'
  fi
}

expected_archive_checksum() {
  local checksum

  checksum="$(awk -v archive="${ARCHIVE}" '$2 == archive {print $1}' "${WORKDIR}/checksums.txt")"
  if [[ -z "${checksum}" ]]; then
    err "no checksum entry found for ${ARCHIVE}"
    exit 1
  fi

  printf "%s" "${checksum}"
}

verify_checksum() {
  local actual expected

  expected="$(expected_archive_checksum)"
  actual="$(hash_file "${WORKDIR}/${ARCHIVE}")"
  if [[ "${actual}" != "${expected}" ]]; then
    err "Checksum verification failed for ${ARCHIVE}."
    err "Expected: ${expected}"
    err "Actual:   ${actual}"
    exit 1
  fi
  CHECKSUM_STATUS="verified"
}

verify_cosign() {
  local bundle="${WORKDIR}/${ARCHIVE}.sigstore.json"

  if [[ ! -f "${bundle}" ]]; then
    if [[ "${REQUIRE_COSIGN}" == "true" ]]; then
      err "cosign verification is required but ${ARCHIVE}.sigstore.json was not found"
      exit 1
    fi
    COSIGN_STATUS="skipped; bundle unavailable"
    warn "${ARCHIVE}.sigstore.json not found; skipped cosign verification"
    return 0
  fi

  if ! command -v cosign >/dev/null 2>&1; then
    if [[ "${REQUIRE_COSIGN}" == "true" ]]; then
      err "cosign verification is required but cosign is not installed"
      exit 1
    fi
    COSIGN_STATUS="skipped; cosign not found"
    warn "cosign not found; skipped Sigstore verification"
    return 0
  fi

  cosign verify-blob \
    --bundle "${bundle}" \
    --certificate-oidc-issuer "${COSIGN_ISSUER}" \
    --certificate-identity "${COSIGN_IDENTITY}" \
    "${WORKDIR}/${ARCHIVE}" >/dev/null
  COSIGN_STATUS="verified"
}

extract_archive() {
  local extract_dir found

  extract_dir="${WORKDIR}/extract"
  mkdir -p "${extract_dir}"
  tar -xzf "${WORKDIR}/${ARCHIVE}" -C "${extract_dir}"

  if [[ -f "${extract_dir}/drydock" ]]; then
    EXTRACTED_BINARY="${extract_dir}/drydock"
  else
    found="$(find "${extract_dir}" -type f -name drydock -perm -u+x | head -n 1)"
    if [[ -z "${found}" ]]; then
      err "archive ${ARCHIVE} does not contain an executable drydock binary"
      exit 1
    fi
    EXTRACTED_BINARY="${found}"
  fi
  chmod 0755 "${EXTRACTED_BINARY}"
}

smoke_binary() {
  local path="$1"
  "${path}" version >/dev/null
}

is_shim_file() {
  local path="$1"

  head -n 5 "${path}" 2>/dev/null | grep -Eiq '(mise|asdf).*shim|shim.*(mise|asdf)'
}

is_package_manager_path() {
  local path="$1"

  case "${path}" in
    /opt/homebrew/*|*/opt/homebrew/*|/home/linuxbrew/.linuxbrew/*|*/home/linuxbrew/.linuxbrew/*|/usr/local/Homebrew/*|/usr/local/Cellar/*|/usr/local/opt/*|/nix/store/*|/snap/*|/var/lib/snapd/*)
      return 0
      ;;
  esac

  return 1
}

is_shim_path() {
  local path="$1"

  case "${path}" in
    */.asdf/shims/*|*/.mise/shims/*|*/.local/share/mise/shims/*|*/shims/drydock)
      return 0
      ;;
  esac

  return 1
}

is_auto_target_allowed() {
  local path="$1"

  [[ "${path}" == /* ]] || return 1
  [[ -f "${path}" ]] || return 1
  [[ ! -L "${path}" ]] || return 1
  ! is_shim_path "${path}" || return 1
  ! is_package_manager_path "${path}" || return 1
  ! is_shim_file "${path}" || return 1
}

resolve_target_path() {
  local default_target existing

  default_target="${DEFAULT_TARGET}"
  if install_testing_enabled && [[ -n "${DRYDOCK_INSTALL_TEST_DEFAULT_TARGET:-}" ]]; then
    default_target="${DRYDOCK_INSTALL_TEST_DEFAULT_TARGET}"
  fi

  if [[ -n "${TARGET}" ]]; then
    TARGET_PATH="${TARGET}"
  elif existing="$(command -v "${PROJECT}" 2>/dev/null)" && is_auto_target_allowed "${existing}"; then
    TARGET_PATH="${existing}"
  else
    TARGET_PATH="${default_target}"
  fi

  if [[ "${TARGET_PATH}" != /* ]]; then
    err "Target path must be absolute: ${TARGET_PATH}"
    exit 1
  fi
}

validate_target_path() {
  if [[ -d "${TARGET_PATH}" ]]; then
    err "Target path must be a file path, got directory: ${TARGET_PATH}"
    exit 1
  fi
}

target_matches_release() {
  local actual expected

  if [[ ! -f "${TARGET_PATH}" ]]; then
    return 1
  fi

  expected="$(hash_file "${EXTRACTED_BINARY}")"
  actual="$(hash_file "${TARGET_PATH}")"
  [[ "${actual}" == "${expected}" ]]
}

maybe_exit_if_current() {
  if [[ "${IF_OUTDATED}" != "true" ]]; then
    return 0
  fi

  if target_matches_release; then
    log "${TARGET_PATH} already matches $(release_label); no update needed."
    exit 0
  fi

  if [[ -e "${TARGET_PATH}" ]]; then
    log "${TARGET_PATH} does not match $(release_label); update needed."
  else
    log "${TARGET_PATH} is missing; install needed."
  fi
}

default_bash_completion_path() {
  if [[ -n "${XDG_DATA_HOME:-}" ]]; then
    printf "%s/bash-completion/completions/drydock" "${XDG_DATA_HOME}"
  elif [[ -n "${HOME:-}" ]]; then
    printf "%s/.local/share/bash-completion/completions/drydock" "${HOME}"
  fi
}

default_zsh_completion_path() {
  if [[ -n "${ZDOTDIR:-}" ]]; then
    printf "%s/.zfunc/_drydock" "${ZDOTDIR}"
  elif [[ -n "${HOME:-}" ]]; then
    printf "%s/.zfunc/_drydock" "${HOME}"
  fi
}

default_fish_completion_path() {
  if [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
    printf "%s/fish/completions/drydock.fish" "${XDG_CONFIG_HOME}"
  elif [[ -n "${HOME:-}" ]]; then
    printf "%s/.config/fish/completions/drydock.fish" "${HOME}"
  fi
}

bash_completion_path() {
  if [[ -n "${BASH_COMPLETION_DIR}" ]]; then
    printf "%s/drydock" "${BASH_COMPLETION_DIR}"
  else
    default_bash_completion_path
  fi
}

zsh_completion_path() {
  if [[ -n "${ZSH_COMPLETION_DIR}" ]]; then
    printf "%s/_drydock" "${ZSH_COMPLETION_DIR}"
  else
    default_zsh_completion_path
  fi
}

fish_completion_path() {
  if [[ -n "${FISH_COMPLETION_DIR}" ]]; then
    printf "%s/drydock.fish" "${FISH_COMPLETION_DIR}"
  else
    default_fish_completion_path
  fi
}

completion_summary() {
  local bash_path fish_path zsh_path

  if [[ "${NO_COMPLETIONS}" == "true" ]]; then
    printf "disabled"
    return 0
  fi

  bash_path="$(bash_completion_path)"
  zsh_path="$(zsh_completion_path)"
  fish_path="$(fish_completion_path)"

  if [[ -z "${bash_path}${zsh_path}${fish_path}" ]]; then
    printf "manual only"
    return 0
  fi

  printf "planned"
  [[ -z "${bash_path}" ]] || printf " bash:%s" "${bash_path}"
  [[ -z "${zsh_path}" ]] || printf " zsh:%s" "${zsh_path}"
  [[ -z "${fish_path}" ]] || printf " fish:%s" "${fish_path}"
}

print_manual_completion_commands() {
  warn "no shell completions were installed automatically"
  warn "manual examples:"
  warn "  drydock completion bash > drydock"
  warn "  drydock completion zsh > _drydock"
  warn "  drydock completion fish > drydock.fish"
}

install_one_completion() {
  local shell="$1"
  local path="$2"
  local explicit="$3"
  local dir tmp

  [[ -n "${path}" ]] || return 2

  dir="$(dirname "${path}")"
  if ! mkdir -p "${dir}" 2>/dev/null; then
    if [[ "${explicit}" == "true" ]]; then
      err "could not create explicit ${shell} completion directory: ${dir}"
      exit 1
    fi
    warn "could not create default ${shell} completion directory: ${dir}; skipping"
    return 1
  fi

  if [[ ! -w "${dir}" ]]; then
    if [[ "${explicit}" == "true" ]]; then
      err "explicit ${shell} completion directory is not writable: ${dir}"
      exit 1
    fi
    warn "default ${shell} completion directory is not writable: ${dir}; skipping"
    return 1
  fi

  tmp="${WORKDIR}/completion-${shell}"
  if ! "${TARGET_PATH}" completion "${shell}" >"${tmp}"; then
    if [[ "${explicit}" == "true" ]]; then
      err "could not generate ${shell} completion"
      exit 1
    fi
    warn "could not generate ${shell} completion; skipping"
    return 1
  fi

  if ! install -m 0644 "${tmp}" "${path}"; then
    if [[ "${explicit}" == "true" ]]; then
      err "could not write explicit ${shell} completion: ${path}"
      exit 1
    fi
    warn "could not write default ${shell} completion: ${path}; skipping"
    return 1
  fi

  log "Installed ${shell} completion to ${path}"
  return 0
}

install_completions() {
  local installed=0
  local bash_path fish_path zsh_path

  if [[ "${NO_COMPLETIONS}" == "true" ]]; then
    return 0
  fi

  bash_path="$(bash_completion_path)"
  zsh_path="$(zsh_completion_path)"
  fish_path="$(fish_completion_path)"

  if install_one_completion bash "${bash_path}" "$([[ -n "${BASH_COMPLETION_DIR}" ]] && printf true || printf false)"; then
    installed=$((installed + 1))
  fi
  if install_one_completion zsh "${zsh_path}" "$([[ -n "${ZSH_COMPLETION_DIR}" ]] && printf true || printf false)"; then
    installed=$((installed + 1))
    if [[ -z "${ZSH_COMPLETION_DIR}" && -n "${zsh_path}" ]]; then
      log "zsh hint: add ${zsh_path%/_drydock} to fpath if your shell does not load the completion."
    fi
  fi
  if install_one_completion fish "${fish_path}" "$([[ -n "${FISH_COMPLETION_DIR}" ]] && printf true || printf false)"; then
    installed=$((installed + 1))
  fi

  if [[ "${installed}" -eq 0 ]]; then
    print_manual_completion_commands
  fi
}

prompt_yes_no() {
  local prompt="$1"
  local answer

  if [[ "${AUTO_YES}" == "true" ]]; then
    return 0
  fi

  if ! { : </dev/tty >/dev/tty; } 2>/dev/null; then
    err "Refusing to modify the filesystem without an interactive terminal or --yes."
    exit 1
  fi

  printf "%s [Y/n] " "${prompt}" >/dev/tty
  IFS= read -r answer </dev/tty || exit 1
  [[ -z "${answer}" || "${answer}" =~ ^[Yy]$ ]]
}

safe_install_mode() {
  local decimal mode="$1"

  while [[ "${#mode}" -gt 1 && "${mode}" == 0* ]]; do
    mode="${mode#0}"
  done

  if [[ -z "${mode}" || ! "${mode}" =~ ^[0-7]+$ ]]; then
    printf "0755"
    return 0
  fi

  decimal=$((8#${mode}))
  decimal=$(((decimal & 0777) | 0111))
  printf "%04o" "${decimal}"
}

target_mode() {
  local raw

  if [[ -e "${TARGET_PATH}" ]]; then
    if stat -c '%a' "${TARGET_PATH}" >/dev/null 2>&1; then
      raw="$(stat -c '%a' "${TARGET_PATH}")"
    else
      raw="$(stat -f '%Lp' "${TARGET_PATH}")"
    fi
    safe_install_mode "${raw}"
  else
    printf "0755"
  fi
}

target_owner_group() {
  if stat -c '%u %g' "${TARGET_PATH}" >/dev/null 2>&1; then
    stat -c '%u %g' "${TARGET_PATH}"
  else
    stat -f '%u %g' "${TARGET_PATH}"
  fi
}

target_needs_sudo() {
  local ancestor dir

  if install_testing_enabled && [[ "${DRYDOCK_INSTALL_TEST_FORCE_SUDO:-}" == "1" ]]; then
    return 0
  fi

  if [[ -e "${TARGET_PATH}" ]]; then
    dir="$(dirname "${TARGET_PATH}")"
    [[ ! -w "${TARGET_PATH}" || ! -w "${dir}" ]]
    return
  fi

  dir="$(dirname "${TARGET_PATH}")"
  ancestor="${dir}"
  while [[ ! -e "${ancestor}" ]]; do
    ancestor="$(dirname "${ancestor}")"
  done

  [[ ! -w "${ancestor}" ]]
}

require_sudo() {
  if [[ "${EUID}" -eq 0 ]] && ! { install_testing_enabled && [[ "${DRYDOCK_INSTALL_TEST_FORCE_SUDO:-}" == "1" ]]; }; then
    return 0
  fi

  if { install_testing_enabled && [[ "${DRYDOCK_INSTALL_TEST_NO_SUDO:-}" == "1" ]]; } || ! command -v sudo >/dev/null 2>&1; then
    err "elevated privileges are required to write ${TARGET_PATH}, but sudo is not available"
    exit 1
  fi

  sudo -v
}

install_binary() {
  local dir group mode owner

  dir="$(dirname "${TARGET_PATH}")"
  mode="$(target_mode)"

  if target_needs_sudo; then
    require_sudo
    sudo mkdir -p "${dir}"
    if [[ -e "${TARGET_PATH}" ]]; then
      read -r owner group < <(target_owner_group)
      sudo install -o "${owner}" -g "${group}" -m "${mode}" "${EXTRACTED_BINARY}" "${TARGET_PATH}"
    else
      sudo install -m "${mode}" "${EXTRACTED_BINARY}" "${TARGET_PATH}"
    fi
  else
    mkdir -p "${dir}"
    install -m "${mode}" "${EXTRACTED_BINARY}" "${TARGET_PATH}"
  fi
}

print_plan() {
  printf "\n"
  printf "Ready to install/update drydock\n"
  printf "\n"
  printf "Release:      %s\n" "$(release_label)"
  printf "Target:       %s\n" "${TARGET_PATH}"
  printf "Archive:      %s\n" "${ARCHIVE}"
  printf "Checksum:     %s\n" "${CHECKSUM_STATUS}"
  printf "Cosign:       %s\n" "${COSIGN_STATUS}"
  printf "Completions:  %s\n" "$(completion_summary)"
  printf "\n"
}

main() {
  parse_args "$@"

  detect_archive
  resolve_target_path
  validate_target_path
  download_assets
  verify_checksum
  verify_cosign
  extract_archive
  smoke_binary "${EXTRACTED_BINARY}"
  maybe_exit_if_current
  print_plan

  if [[ "${DRY_RUN}" == "true" ]]; then
    log "Dry run complete. No files changed."
    return 0
  fi

  if ! prompt_yes_no "Continue with install/update?"; then
    err "Aborted."
    exit 1
  fi

  install_binary
  smoke_binary "${TARGET_PATH}"
  install_completions
  log "Completed successfully."
}

main "$@"
