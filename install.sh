#!/bin/sh
#
# Signet installer.
#
#   curl -fsSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh
#
# Downloads the release binary for this platform, verifies it against the
# release checksums, and installs it. Run with --help for options.

set -e

INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-latest}"
BINARY_NAME="signet"
GITHUB_REPO="Vulnetix/signet"
GITHUB_BASE="https://github.com/${GITHUB_REPO}/releases"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    --install-dir)
      INSTALL_DIR="$2"
      shift 2
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --help)
      cat <<EOF
Usage: install.sh [options]

Options:
  --install-dir DIR    Installation directory (default: /usr/local/bin)
  --version VERSION    Version to install, e.g. v0.1.1 (default: latest)
  --help               Show this message

Environment variables:
  INSTALL_DIR          Overrides --install-dir
  VERSION              Overrides --version

Examples:
  curl -fsSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh
  curl -fsSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh -s -- --install-dir ~/.local/bin
  curl -fsSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh -s -- --version v0.1.1
EOF
      exit 0
      ;;
    *)
      echo "error: unknown option: $1" >&2
      echo "Run with --help for usage." >&2
      exit 1
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Environment detection
# ---------------------------------------------------------------------------

detect_os() {
  case "$(uname -s 2>/dev/null)" in
    Linux)                echo "linux" ;;
    Darwin)               echo "darwin" ;;
    CYGWIN*|MINGW*|MSYS*) echo "windows" ;;
    *)
      echo "error: unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

# Signet publishes amd64 and arm64 only. Anything else is named explicitly so
# the failure says what is missing rather than 404ing on an asset URL later.
detect_arch() {
  case "$(uname -m 2>/dev/null)" in
    x86_64|amd64)  echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "error: unsupported architecture: $(uname -m)" >&2
      echo "error: Signet releases cover amd64 and arm64. Build from source:" >&2
      echo "error:   go install github.com/vulnetix/signet/cmd/signet@latest" >&2
      exit 1
      ;;
  esac
}

detect_downloader() {
  if command -v curl >/dev/null 2>&1; then
    echo "curl"
  elif command -v wget >/dev/null 2>&1; then
    echo "wget"
  else
    echo "error: curl or wget is required" >&2
    exit 1
  fi
}

# Detect an available sha256 tool, ordered most to least commonly available:
#   sha256sum  — GNU coreutils (Linux, Alpine/busybox, Git for Windows/MSYS)
#   shasum     — Perl digest tool (macOS built-in, most Unix with Perl)
#   sha256     — BSD native (FreeBSD, OpenBSD, NetBSD)
#   openssl    — widely available fallback
#   python3    — modern systems
#   rhash      — some Linux distros (Fedora, Arch)
#   certutil   — Windows (CYGWIN/MSYS/MinGW) last resort
detect_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    echo "shasum"
  elif command -v sha256 >/dev/null 2>&1; then
    echo "sha256"
  elif command -v openssl >/dev/null 2>&1; then
    echo "openssl"
  elif command -v python3 >/dev/null 2>&1; then
    echo "python3"
  elif command -v rhash >/dev/null 2>&1; then
    echo "rhash"
  elif command -v certutil >/dev/null 2>&1; then
    echo "certutil"
  else
    echo "none"
  fi
}

compute_sha256() {
  file="$1"
  tool="$2"
  case "$tool" in
    sha256sum) sha256sum "$file" | awk '{print $1}' ;;
    shasum)    shasum -a 256 "$file" | awk '{print $1}' ;;
    sha256)    sha256 -q "$file" ;;
    openssl)   openssl dgst -sha256 "$file" | awk '{print $NF}' ;;
    python3)   python3 -c "import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest())" "$file" ;;
    rhash)     rhash --sha256 --printf='%h\n' "$file" ;;
    certutil)  certutil -hashfile "$file" SHA256 2>/dev/null | grep -v "^SHA256" | grep -v "CertUtil:" | tr -d ' \r\n' ;;
  esac
}

check_existing() {
  target="$1"
  if [ -f "$target" ]; then
    existing_ver="$("$target" -version 2>/dev/null | head -1 || true)"
    echo "info: existing binary found: ${existing_ver:-unknown version}"
    echo "info: will be overwritten at $target"
  fi
}

resolve_install_dir() {
  dir="$1"
  if mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; then
    echo "$dir"
    return
  fi
  fallback="$HOME/.local/bin"
  echo "warn: $dir is not writable, falling back to $fallback" >&2
  mkdir -p "$fallback"
  echo "$fallback"
}

download() {
  url="$1"
  dest="$2"
  downloader="$3"
  if [ "$downloader" = "curl" ]; then
    # --retry-all-errors is what makes this retry a TRUNCATED transfer. curl
    # reports an incomplete body as exit 18, which plain --retry treats as a
    # hard failure rather than something to try again.
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors "$url" -o "$dest"
  else
    wget -q --tries=5 --timeout=30 --waitretry=2 "$url" -O "$dest"
  fi
}

# resolve_latest_tag turns the /releases/latest redirect into a concrete tag.
#
# Uses the redirect rather than the API so it needs no token and is not subject
# to the API rate limit, which an unauthenticated CI runner will hit. Prints
# nothing when it cannot resolve, and the caller falls back.
resolve_latest_tag() {
  url=""
  if command -v curl >/dev/null 2>&1; then
    url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' --retry 2 \
      "${GITHUB_BASE}/latest" 2>/dev/null)
  elif command -v wget >/dev/null 2>&1; then
    url=$(wget -q --spider --server-response --max-redirect=10 \
      "${GITHUB_BASE}/latest" 2>&1 \
      | awk '/^  Location: /{print $2}' | tail -n1)
  fi

  case "$url" in
    */tag/v*) printf '%s' "${url##*/tag/}" ;;
    *) printf '' ;;
  esac
}

# expected_size reports the asset's size from the server, or nothing when the
# server will not say. Used to tell an interrupted download apart from a
# genuine integrity failure before the checksum is even computed.
expected_size() {
  url="$1"
  downloader="$2"
  if [ "$downloader" = "curl" ]; then
    curl -fsSLI --retry 2 "$url" 2>/dev/null \
      | tr -d '\r' | awk 'tolower($1) == "content-length:" { print $2 }' | tail -n1
  else
    wget -q --spider --server-response "$url" 2>&1 \
      | tr -d '\r' | awk 'tolower($1) == "content-length:" { print $2 }' | tail -n1
  fi
}

# verify_size checks the download arrived whole, so a dropped connection is
# reported as a network problem rather than surfacing later as a checksum
# mismatch that reads like tampering.
verify_size() {
  path="$1"
  want="${2:-}"

  if [ ! -f "$path" ]; then
    echo "error: downloaded file not found at $path" >&2
    exit 1
  fi
  size=$(wc -c < "$path" 2>/dev/null || echo 0)
  if [ "$size" -lt 1024 ]; then
    echo "error: downloaded file is suspiciously small (${size} bytes) — download may have failed" >&2
    rm -f "$path"
    exit 1
  fi
  if [ -n "$want" ] && [ "$want" -gt 0 ] 2>/dev/null && [ "$size" -ne "$want" ]; then
    echo "error: download is incomplete — got ${size} bytes, server declared ${want}" >&2
    echo "error: this is a network problem, not a corrupt release. Please run the installer again." >&2
    rm -f "$path"
    exit 1
  fi
  echo "info: download size=${size} bytes"
}

# verify_checksum fails closed: an asset listed in checksums.txt whose digest
# does not match is never installed.
verify_checksum() {
  asset="$1"
  tmp_binary="$2"
  checksums_url="$3"
  downloader="$4"
  sha_tool="$5"

  if [ "$sha_tool" = "none" ]; then
    echo "warn: no sha256 tool found (sha256sum/shasum/openssl), skipping checksum verification" >&2
    return
  fi

  tmp_checksums=$(mktemp)

  echo "info: fetching $checksums_url"
  if ! download "$checksums_url" "$tmp_checksums" "$downloader" 2>&1; then
    echo "warn: could not fetch checksums.txt, skipping checksum verification" >&2
    rm -f "$tmp_checksums"
    return
  fi

  expected=$(grep " ${asset}\$" "$tmp_checksums" | awk '{print $1}')
  rm -f "$tmp_checksums"

  if [ -z "$expected" ]; then
    echo "warn: asset '${asset}' not found in checksums.txt, skipping checksum verification" >&2
    return
  fi

  echo "info: expected sha256=$expected"

  actual=$(compute_sha256 "$tmp_binary" "$sha_tool")
  echo "info: actual   sha256=$actual"

  if [ "$actual" != "$expected" ]; then
    echo "error: checksum mismatch — refusing to install" >&2
    echo "error:   expected: $expected" >&2
    echo "error:   actual:   $actual" >&2
    echo "error:   size:     $(wc -c < "$tmp_binary" 2>/dev/null || echo unknown) bytes" >&2
    echo "error: if the size above looks short, the download was interrupted — run the installer again." >&2
    echo "error: if it is the full size, do not install this binary. Report it at" >&2
    echo "error:   https://github.com/Vulnetix/signet/issues" >&2
    rm -f "$tmp_binary"
    exit 1
  fi

  echo "info: checksum verified ok"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
  echo "signet installer"
  echo "----------------"

  OS=$(detect_os)
  ARCH=$(detect_arch)
  PLATFORM="${OS}-${ARCH}"
  DOWNLOADER=$(detect_downloader)
  SHA_TOOL=$(detect_sha256)
  echo "info: os=$OS arch=$ARCH platform=$PLATFORM downloader=$DOWNLOADER sha256=$SHA_TOOL"

  INSTALL_DIR=$(resolve_install_dir "$INSTALL_DIR")
  BINARY_PATH="${INSTALL_DIR}/${BINARY_NAME}"
  echo "info: install dir=$INSTALL_DIR"

  check_existing "$BINARY_PATH"

  EXT=""
  [ "$OS" = "windows" ] && EXT=".exe"
  ASSET="${BINARY_NAME}-${PLATFORM}${EXT}"

  # `latest` is resolved to a concrete tag ONCE, and both downloads are then
  # pinned to it. Fetching the binary and its checksums through the /latest/
  # alias is not atomic: a release published between the two moves the alias,
  # so the bytes come from one release and the digest from the next, and the
  # installer reports a checksum mismatch on two files that are each intact.
  if [ "$VERSION" = "latest" ]; then
    RESOLVED=$(resolve_latest_tag)
    if [ -n "$RESOLVED" ]; then
      VERSION="$RESOLVED"
    else
      echo "warn: could not resolve the latest tag; falling back to the /latest/ alias." >&2
      echo "warn: if a release lands mid-download this can report a checksum mismatch — retry if it does." >&2
    fi
  fi

  if [ "$VERSION" = "latest" ]; then
    DOWNLOAD_URL="${GITHUB_BASE}/latest/download/${ASSET}"
    CHECKSUMS_URL="${GITHUB_BASE}/latest/download/checksums.txt"
  else
    DOWNLOAD_URL="${GITHUB_BASE}/download/${VERSION}/${ASSET}"
    CHECKSUMS_URL="${GITHUB_BASE}/download/${VERSION}/checksums.txt"
  fi

  echo "info: version=$VERSION asset=$ASSET"
  echo "info: binary    $DOWNLOAD_URL"
  echo "info: checksums $CHECKSUMS_URL"

  TMP_FILE=$(mktemp "${INSTALL_DIR}/.${BINARY_NAME}.XXXXXX" 2>/dev/null || mktemp)
  trap 'rm -f "$TMP_FILE"' EXIT INT TERM

  echo "info: fetching $DOWNLOAD_URL"
  # Asked before the transfer so an interrupted download is diagnosed as one.
  # Best effort: a server that will not declare a length leaves this empty and
  # verify_size falls back to its floor check.
  EXPECT_BYTES=$(expected_size "$DOWNLOAD_URL" "$DOWNLOADER" || true)
  download "$DOWNLOAD_URL" "$TMP_FILE" "$DOWNLOADER"
  verify_size "$TMP_FILE" "$EXPECT_BYTES"
  verify_checksum "$ASSET" "$TMP_FILE" "$CHECKSUMS_URL" "$DOWNLOADER" "$SHA_TOOL"

  chmod +x "$TMP_FILE"
  mv "$TMP_FILE" "$BINARY_PATH"
  trap - EXIT INT TERM

  echo "info: installed to $BINARY_PATH"

  INSTALLED_VER=$("$BINARY_PATH" -version 2>/dev/null | head -1 || true)
  if [ -n "$INSTALLED_VER" ]; then
    echo "info: verified: $INSTALLED_VER"
  else
    echo "warn: binary installed but -version produced no output" >&2
  fi

  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      echo ""
      echo "warn: $INSTALL_DIR is not in PATH"
      echo "      add to your shell profile:"
      echo "      export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac

  echo ""
  echo "run:  $BINARY_NAME             # start the terminal UI"
  echo "      $BINARY_NAME -version"
  echo ""
  echo "Set an API key first, e.g. OPENAI_API_KEY or ANTHROPIC_API_KEY, or run"
  echo "/credentials inside the UI. See https://github.com/Vulnetix/signet"
}

main "$@"
