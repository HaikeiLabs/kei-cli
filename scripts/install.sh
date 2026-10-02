#!/bin/sh
set -eu

# install.sh — Download and install the kei CLI (and optionally kei-proxy)
# from AWS S3.
#
# The script detects the OS and architecture, downloads the matching release
# archive from S3, verifies its SHA-256 checksum (and, for the kei-cli
# archive, an optional GPG signature), and installs the binaries to the target
# directory. Each install records the installed files and version in a
# .kei-install-manifest in the install directory; --uninstall removes exactly
# those files.
#
# Usage:
#   export AWS_S3_RELEASES_URL_BASE=https://kei-cli-releases.s3.us-east-1.amazonaws.com
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | sh
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | sh -s -- -d ~/.local/bin
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | sh -s -- -v 0.2.0 -d ~/.local/bin
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | sh -s -- --proxy-only -d ~/.local/bin
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | sh -s -- --uninstall -d ~/.local/bin
#
# Flags:
#   -v VERSION      Version to install (default: latest for kei; the pinned
#                   kei-proxy version for --proxy-only)
#   -d DIR          Install directory (default: /usr/local/bin)
#   -t TMPDIR       Temporary directory (default: mktemp -d)
#   --proxy-only    Install only kei-proxy, from the standalone kei-proxy
#                   release archives at <base>/kei-proxy/<version>/
#   --uninstall     Remove the files recorded in the install manifest
#   -h, --help      Show this help
#
# Optional configuration:
#
#   AWS_S3_RELEASES_URL_BASE — Public S3 endpoint for curl downloads.
#                              Defaults to:
#                                https://kei-cli-releases.s3.us-east-1.amazonaws.com
#                              (bucket: kei-cli-releases, region: us-east-1).
#                              Override for a mirror or CloudFront distribution.
#
#   KEI_PROXY_VERSION — Default kei-proxy version for --proxy-only when -v is
#                       not given. Resolved, in order, from this variable, a
#                       KEI_PROXY_VERSION file in the current directory, or
#                       the copy published alongside install.sh at
#                       <base>/kei-cli/KEI_PROXY_VERSION.
#
# The install URL is constructed as:
#   $AWS_S3_RELEASES_URL_BASE/kei-cli/<version>/<artifact>
#   $AWS_S3_RELEASES_URL_BASE/kei-proxy/<version>/<artifact>
#
# The default endpoint is public. If a CloudFront/CDN distribution is used,
# override AWS_S3_RELEASES_URL_BASE with its URL.

MANIFEST_NAME=".kei-install-manifest"

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

Options:
  -v VERSION      Version to install (default: latest for kei; the pinned
                  kei-proxy version for --proxy-only)
  -d DIR          Install directory (default: /usr/local/bin)
  -t TMPDIR       Temporary directory (default: mktemp -d)
  --proxy-only    Install only kei-proxy, from the standalone kei-proxy
                  release archives at <base>/kei-proxy/<version>/
  --uninstall     Remove the files recorded in the install manifest
  -h, --help      Show this help

Environment:
  AWS_S3_RELEASES_URL_BASE  S3 base URL (default: the public kei-cli-releases
                            endpoint)
  KEI_PROXY_VERSION         Default kei-proxy version for --proxy-only
USAGE
}

# ---- Parse flags -----------------------------------------------------------
VERSION="latest"
INSTALL_DIR="/usr/local/bin"
TMP_DIR=""
MODE="install"

while [ $# -gt 0 ]; do
  case "$1" in
    -v)
      if [ $# -lt 2 ]; then
        echo "Error: -v requires a VERSION argument." >&2
        exit 2
      fi
      VERSION="$2"
      shift 2
      ;;
    -d)
      if [ $# -lt 2 ]; then
        echo "Error: -d requires a DIR argument." >&2
        exit 2
      fi
      INSTALL_DIR="$2"
      shift 2
      ;;
    -t)
      if [ $# -lt 2 ]; then
        echo "Error: -t requires a TMPDIR argument." >&2
        exit 2
      fi
      TMP_DIR="$2"
      shift 2
      ;;
    --proxy-only)
      if [ "$MODE" = "uninstall" ]; then
        echo "Error: --proxy-only and --uninstall are mutually exclusive." >&2
        exit 2
      fi
      MODE="proxy-only"
      shift
      ;;
    --uninstall)
      if [ "$MODE" = "proxy-only" ]; then
        echo "Error: --uninstall and --proxy-only are mutually exclusive." >&2
        exit 2
      fi
      MODE="uninstall"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      while [ $# -gt 0 ]; do
        shift
      done
      ;;
    -*)
      echo "Error: invalid option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      echo "Error: unexpected argument: $1" >&2
      exit 2
      ;;
  esac
done

# ---- Uninstall (no downloads needed) ---------------------------------------
run_uninstall() {
  UNINSTALL_MANIFEST="$INSTALL_DIR/$MANIFEST_NAME"
  if [ ! -f "$UNINSTALL_MANIFEST" ]; then
    echo "Error: no install manifest found at $UNINSTALL_MANIFEST." >&2
    echo "Nothing to uninstall in $INSTALL_DIR." >&2
    exit 1
  fi
  UNINSTALL_REMOVED=0
  while IFS= read -r UNINSTALL_LINE || [ -n "$UNINSTALL_LINE" ]; do
    case "$UNINSTALL_LINE" in
      ''|'#'*) continue ;;
      */*) echo "Skipping unsafe manifest entry: $UNINSTALL_LINE" >&2; continue ;;
    esac
    UNINSTALL_TARGET="$INSTALL_DIR/$UNINSTALL_LINE"
    if [ -e "$UNINSTALL_TARGET" ] || [ -L "$UNINSTALL_TARGET" ]; then
      rm -f "$UNINSTALL_TARGET"
      echo "Removed $UNINSTALL_TARGET" >&2
      UNINSTALL_REMOVED=1
    else
      echo "Not present (skipped): $UNINSTALL_TARGET" >&2
    fi
  done < "$UNINSTALL_MANIFEST"
  if [ "$UNINSTALL_REMOVED" -eq 0 ]; then
    echo "Warning: manifest listed no removable files." >&2
  fi
  rm -f "$UNINSTALL_MANIFEST"
  echo "Removed $UNINSTALL_MANIFEST" >&2
  echo "Uninstalled kei from $INSTALL_DIR" >&2
}

if [ "$MODE" = "uninstall" ]; then
  run_uninstall
  exit 0
fi

# ---- Release endpoint ------------------------------------------------------
AWS_S3_RELEASES_URL_BASE="${AWS_S3_RELEASES_URL_BASE:-https://kei-cli-releases.s3.us-east-1.amazonaws.com}"

# ---- Detect platform -------------------------------------------------------
OS=""
ARCH=""

case "$(uname -s)" in
  Darwin)  OS="macOS" ;;
  Linux)   OS="Linux" ;;
  *)
    echo "Error: unsupported OS ($(uname -s)). kei-cli supports macOS and Linux." >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="x86_64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Error: unsupported architecture ($(uname -m)). kei-cli supports amd64 and arm64." >&2
    exit 1
    ;;
esac

# Rosetta hint on Darwin
if [ "$OS" = "macOS" ] && [ "$ARCH" = "x86_64" ]; then
  if [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = "1" ]; then
    echo "Warning: Running under Rosetta 2 on Apple Silicon." >&2
    echo "  For a native arm64 build, use: arch -arm64 curl -fsSL ... | sh" >&2
  fi
fi

# ---- Detect checksum tool --------------------------------------------------
SHA_CMD=""
if command -v shasum >/dev/null 2>&1; then
  SHA_CMD="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
  SHA_CMD="sha256sum"
else
  echo "Error: no SHA-256 checksum tool found (tried shasum, sha256sum)." >&2
  echo "Install one of: coreutils (macOS), sha256sum (Linux)." >&2
  exit 1
fi

# ---- Checksum verification -------------------------------------------------
# Verify the SHA-256 checksum of an archive against a checksums file.
# Args: $1 = directory containing the files, $2 = checksums file basename,
#       $3 = archive basename to verify. Exits non-zero on mismatch.
verify_checksum() {
  VC_DIR="$1"
  VC_CHECKSUMS="$2"
  VC_ARCHIVE="$3"
  VC_MANUAL=""
  if [ "$SHA_CMD" = "shasum -a 256" ]; then
    (cd "$VC_DIR" && shasum -a 256 -c "$VC_CHECKSUMS" --ignore-missing 2>/dev/null) || VC_MANUAL=1
  elif [ "$SHA_CMD" = "sha256sum" ]; then
    (cd "$VC_DIR" && sha256sum -c "$VC_CHECKSUMS" --ignore-missing 2>/dev/null) || VC_MANUAL=1
  else
    VC_MANUAL=1
  fi
  if [ "${VC_MANUAL:-0}" = "1" ]; then
    VC_EXPECTED="$(grep "$VC_ARCHIVE" "$VC_DIR/$VC_CHECKSUMS" | awk '{print $1}')"
    if [ -z "$VC_EXPECTED" ]; then
      echo "Error: $VC_ARCHIVE not found in checksums file." >&2
      exit 1
    fi
    VC_GOT="$($SHA_CMD "$VC_DIR/$VC_ARCHIVE" | awk '{print $1}')"
    if [ "$VC_EXPECTED" != "$VC_GOT" ]; then
      echo "Error: checksum mismatch for $VC_ARCHIVE" >&2
      echo "  Expected: $VC_EXPECTED" >&2
      echo "  Got:      $VC_GOT" >&2
      exit 1
    fi
  fi
}

# Strip a single leading 'v' from a version string (v0.1.15 -> 0.1.15).
strip_v() {
  case "$1" in
    v*) printf '%s' "${1#v}" ;;
    *)  printf '%s' "$1" ;;
  esac
}

# ---- Temporary directory ---------------------------------------------------
if [ -z "${TMP_DIR:-}" ]; then
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
else
  mkdir -p "$TMP_DIR"
fi

# ---- Install kei (default mode) --------------------------------------------
if [ "$MODE" = "install" ]; then
  PROJECT="kei-cli"

  # Resolve latest version
  if [ "$VERSION" = "latest" ]; then
    LATEST_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/latest.txt"
    RESOLVED="$(curl -fsSL --connect-timeout 10 "$LATEST_URL" 2>/dev/null || true)"
    if [ -n "$RESOLVED" ]; then
      VERSION="$RESOLVED"
      echo "Resolved latest version: $VERSION" >&2
    else
      echo "Warning: could not resolve latest version from $LATEST_URL" >&2
      echo "Pass an explicit version with -v VERSION" >&2
      exit 1
    fi
  fi

  ARCHIVE_NAME="${PROJECT}_${VERSION}_${OS}_${ARCH}.tar.gz"
  CHECKSUM_NAME="${PROJECT}_${VERSION}_checksums.txt"
  SIGNATURE_NAME="${CHECKSUM_NAME}.sig"
  ARCHIVE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$ARCHIVE_NAME"
  CHECKSUM_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$CHECKSUM_NAME"
  SIGNATURE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$SIGNATURE_NAME"

  echo "Downloading $ARCHIVE_NAME..." >&2
  curl -fsSL --connect-timeout 15 --retry 3 "$ARCHIVE_URL" -o "$TMP_DIR/$ARCHIVE_NAME"

  echo "Downloading checksums..." >&2
  curl -fsSL --connect-timeout 15 --retry 3 "$CHECKSUM_URL" -o "$TMP_DIR/$CHECKSUM_NAME"

  # Verify GPG signature (optional)
  # The checksums file is signed with the kei-cli-releases@haikeilabs.com GPG key.
  # If the signature and GPG are available, verify; otherwise warn and skip.
  if command -v gpg >/dev/null 2>&1; then
    if curl -fsSL --connect-timeout 10 "$SIGNATURE_URL" -o "$TMP_DIR/$SIGNATURE_NAME" 2>/dev/null; then
      echo "Verifying GPG signature..." >&2
      if gpg --verify "$TMP_DIR/$SIGNATURE_NAME" "$TMP_DIR/$CHECKSUM_NAME" 2>/dev/null; then
        echo "GPG signature verified." >&2
      else
        echo "Warning: GPG signature verification failed." >&2
        echo "The checksums file may be tampered with or the signing key is unknown." >&2
        echo "Import the kei release key: gpg --recv-keys <KEYID>" >&2
        echo "Proceeding with checksum verification only." >&2
      fi
    else
      echo "GPG signature not available for this release; skipping verification." >&2
    fi
  else
    echo "GPG not found; skipping signature verification." >&2
  fi

  echo "Verifying checksum..." >&2
  verify_checksum "$TMP_DIR" "$CHECKSUM_NAME" "$ARCHIVE_NAME"

  echo "Extracting..." >&2
  tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

  BINARY="$TMP_DIR/kei"
  if [ ! -f "$BINARY" ]; then
    echo "Error: kei binary not found in archive." >&2
    exit 1
  fi

  mkdir -p "$INSTALL_DIR"

  # Check that INSTALL_DIR is writable
  if ! touch "$INSTALL_DIR/.kei-write-test" 2>/dev/null; then
    echo "Permission denied writing to $INSTALL_DIR. Re-run with -d \"\$HOME/.local/bin\" or with sudo." >&2
    exit 1
  fi
  rm -f "$INSTALL_DIR/.kei-write-test"

  install -m 0755 "$BINARY" "$INSTALL_DIR/kei"

  PROXY_BINARY="$TMP_DIR/kei-proxy"
  if [ -f "$PROXY_BINARY" ]; then
    install -m 0755 "$PROXY_BINARY" "$INSTALL_DIR/kei-proxy"
    echo "Installed kei-proxy to $INSTALL_DIR/kei-proxy" >&2
  fi

  echo "Installed kei $VERSION to $INSTALL_DIR/kei" >&2

  # Record what was installed so --uninstall can remove exactly these files.
  {
    echo "# kei-install-manifest"
    echo "# version=$VERSION"
    echo "# mode=install"
    echo "kei"
    if [ -x "$INSTALL_DIR/kei-proxy" ]; then
      echo "kei-proxy"
    fi
  } > "$INSTALL_DIR/$MANIFEST_NAME"
  echo "Wrote $INSTALL_DIR/$MANIFEST_NAME" >&2

  echo "Ensure $INSTALL_DIR is on your PATH." >&2

  # Verify installation
  if [ -x "$INSTALL_DIR/kei" ]; then
    INSTALLED_VERSION="$("$INSTALL_DIR/kei" --version 2>/dev/null || true)"
    echo "Verified: $INSTALLED_VERSION" >&2
  fi
  if [ -x "$INSTALL_DIR/kei-proxy" ]; then
    PROXY_VERSION_OUT="$("$INSTALL_DIR/kei-proxy" --version 2>/dev/null || true)"
    echo "Verified: $PROXY_VERSION_OUT" >&2
  fi
  exit 0
fi

# ---- Install kei-proxy only (--proxy-only) ---------------------------------
if [ "$MODE" = "proxy-only" ]; then
  PROJECT="kei-proxy"

  # Resolve the proxy version: an explicit -v, else the pinned default.
  if [ "$VERSION" = "latest" ]; then
    if [ -n "${KEI_PROXY_VERSION:-}" ]; then
      VERSION="$KEI_PROXY_VERSION"
      echo "Using KEI_PROXY_VERSION from environment: $VERSION" >&2
    elif [ -f "KEI_PROXY_VERSION" ]; then
      VERSION="$(tr -d '[:space:]' < KEI_PROXY_VERSION)"
      echo "Using KEI_PROXY_VERSION from repo file: $VERSION" >&2
    else
      PIN_URL="$AWS_S3_RELEASES_URL_BASE/kei-cli/KEI_PROXY_VERSION"
      RESOLVED="$(curl -fsSL --connect-timeout 10 "$PIN_URL" 2>/dev/null || true)"
      if [ -n "$RESOLVED" ]; then
        VERSION="$(printf '%s' "$RESOLVED" | tr -d '[:space:]')"
        echo "Using KEI_PROXY_VERSION published alongside install.sh: $VERSION" >&2
      else
        echo "Error: could not determine the default kei-proxy version." >&2
        echo "Pass an explicit version with -v VERSION, set KEI_PROXY_VERSION, or place a KEI_PROXY_VERSION file in the current directory." >&2
        exit 1
      fi
    fi
  fi

  PROXY_VERSION="$(strip_v "$VERSION")"

  ARCHIVE_NAME="${PROJECT}_${PROXY_VERSION}_${OS}_${ARCH}.tar.gz"
  CHECKSUM_NAME="${PROJECT}_${PROXY_VERSION}_checksums.txt"
  ARCHIVE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$PROXY_VERSION/$ARCHIVE_NAME"
  CHECKSUM_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$PROXY_VERSION/$CHECKSUM_NAME"

  echo "Downloading $ARCHIVE_NAME..." >&2
  curl -fsSL --connect-timeout 15 --retry 3 "$ARCHIVE_URL" -o "$TMP_DIR/$ARCHIVE_NAME"

  echo "Downloading checksums..." >&2
  curl -fsSL --connect-timeout 15 --retry 3 "$CHECKSUM_URL" -o "$TMP_DIR/$CHECKSUM_NAME"

  echo "Verifying checksum..." >&2
  verify_checksum "$TMP_DIR" "$CHECKSUM_NAME" "$ARCHIVE_NAME"

  echo "Extracting..." >&2
  tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

  PROXY_BINARY="$TMP_DIR/kei-proxy"
  if [ ! -f "$PROXY_BINARY" ]; then
    echo "Error: kei-proxy binary not found in archive." >&2
    exit 1
  fi

  mkdir -p "$INSTALL_DIR"

  # Check that INSTALL_DIR is writable
  if ! touch "$INSTALL_DIR/.kei-write-test" 2>/dev/null; then
    echo "Permission denied writing to $INSTALL_DIR. Re-run with -d \"\$HOME/.local/bin\" or with sudo." >&2
    exit 1
  fi
  rm -f "$INSTALL_DIR/.kei-write-test"

  install -m 0755 "$PROXY_BINARY" "$INSTALL_DIR/kei-proxy"
  echo "Installed kei-proxy $PROXY_VERSION to $INSTALL_DIR/kei-proxy" >&2

  # Record what was installed so --uninstall can remove exactly these files.
  {
    echo "# kei-install-manifest"
    echo "# version=$PROXY_VERSION"
    echo "# mode=proxy-only"
    echo "kei-proxy"
  } > "$INSTALL_DIR/$MANIFEST_NAME"
  echo "Wrote $INSTALL_DIR/$MANIFEST_NAME" >&2

  echo "Ensure $INSTALL_DIR is on your PATH." >&2

  if [ -x "$INSTALL_DIR/kei-proxy" ]; then
    PROXY_VERSION_OUT="$("$INSTALL_DIR/kei-proxy" --version 2>/dev/null || true)"
    echo "Verified: $PROXY_VERSION_OUT" >&2
  fi
  exit 0
fi

echo "Error: unknown mode: $MODE" >&2
exit 2
