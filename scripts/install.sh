#!/bin/bash
set -euo pipefail

# install.sh — Download and install the kei CLI from AWS S3.
#
# The script detects the OS and architecture, downloads the matching
# release archive from S3, verifies its SHA-256 checksum and GPG
# signature, and installs the kei binary to the target directory.
#
# Usage:
#   export AWS_S3_RELEASES_URL_BASE=https://kei-cli-releases.s3.us-east-1.amazonaws.com
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash -s -- -d ~/.local/bin
#   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash -s -- -v 0.2.0
#
# Flags:
#   -v VERSION   Version tag to install (default: latest)
#   -d DIR       Install directory (default: /usr/local/bin)
#   -t TMPDIR    Temporary directory (default: mktemp -d)
#
# Optional configuration:
#
#   AWS_S3_RELEASES_URL_BASE — Public S3 endpoint for curl downloads.
#                              Format:
#                                https://<bucket>.s3.<region>.amazonaws.com
#                              or a CloudFront distribution URL.
#                              Defaults to the public kei-cli release endpoint.
#
# The install URL is constructed as:
#   $AWS_S3_RELEASES_URL_BASE/kei-cli/<version>/<artifact>
#
# The default endpoint is public. If a CloudFront/CDN distribution is used,
# override AWS_S3_RELEASES_URL_BASE with its URL.

# ---- Parse flags -----------------------------------------------------------
VERSION="latest"
INSTALL_DIR="/usr/local/bin"

while getopts ":v:d:t:h" opt; do
  case $opt in
    v) VERSION="$OPTARG" ;;
    d) INSTALL_DIR="$OPTARG" ;;
    t) TMP_DIR="$OPTARG" ;;
    h)
      echo "Usage: $(basename "$0") [-v VERSION] [-d DIR] [-t TMPDIR]"
      exit 0
      ;;
    \?)
      echo "Invalid option: -$OPTARG" >&2
      exit 2
      ;;
  esac
done

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

PROJECT="kei-cli"

# ---- Detect checksum tool --------------------------------------------------
SHA_CMD=""
if command -v shasum &>/dev/null; then
  SHA_CMD="shasum -a 256"
elif command -v sha256sum &>/dev/null; then
  SHA_CMD="sha256sum"
else
  echo "Error: no SHA-256 checksum tool found (tried shasum, sha256sum)." >&2
  echo "Install one of: coreutils (macOS), sha256sum (Linux)." >&2
  exit 1
fi

# ---- Resolve latest version ------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  # Fetch the latest version from the S3 bucket listing. This assumes the
  # bucket listing is enabled (or a CDN lists directories). If the bucket
  # does not list keys, pass an explicit version via -v.
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

# ---- Build artifact names --------------------------------------------------
ARCHIVE_NAME="${PROJECT}_${VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUM_NAME="${PROJECT}_${VERSION}_checksums.txt"
SIGNATURE_NAME="${CHECKSUM_NAME}.sig"
ARCHIVE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$ARCHIVE_NAME"
CHECKSUM_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$CHECKSUM_NAME"
SIGNATURE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$SIGNATURE_NAME"

# ---- Temporary directory ---------------------------------------------------
if [ -z "${TMP_DIR:-}" ]; then
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
else
  mkdir -p "$TMP_DIR"
fi

# ---- Download artifacts ----------------------------------------------------
echo "Downloading $ARCHIVE_NAME..." >&2
curl -fsSL --connect-timeout 15 --retry 3 "$ARCHIVE_URL" -o "$TMP_DIR/$ARCHIVE_NAME"

echo "Downloading checksums..." >&2
curl -fsSL --connect-timeout 15 --retry 3 "$CHECKSUM_URL" -o "$TMP_DIR/$CHECKSUM_NAME"

# ---- Verify GPG signature (optional) ---------------------------------------
# The checksums file is signed with the kei-cli-releases@haikeilabs.com GPG key.
# If the signature and GPG are available, verify; otherwise warn and skip.
if command -v gpg &>/dev/null; then
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

# ---- Verify checksum -------------------------------------------------------
echo "Verifying checksum..." >&2
# Try batch verification first (shasum -c / sha256sum -c), then fall back to
# manual comparison for portability.
if [ "$SHA_CMD" = "shasum -a 256" ]; then
  (cd "$TMP_DIR" && shasum -a 256 -c "$CHECKSUM_NAME" --ignore-missing 2>/dev/null) || {
    MANUAL_VERIFY=1
  }
elif [ "$SHA_CMD" = "sha256sum" ]; then
  (cd "$TMP_DIR" && sha256sum -c "$CHECKSUM_NAME" --ignore-missing 2>/dev/null) || {
    MANUAL_VERIFY=1
  }
else
  MANUAL_VERIFY=1
fi

if [ "${MANUAL_VERIFY:-0}" = "1" ]; then
  EXPECTED="$(grep "$ARCHIVE_NAME" "$TMP_DIR/$CHECKSUM_NAME" | awk '{print $1}')"
  if [ -z "$EXPECTED" ]; then
    echo "Error: $ARCHIVE_NAME not found in checksums file." >&2
    exit 1
  fi
  GOT="$($SHA_CMD "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
  if [ "$EXPECTED" != "$GOT" ]; then
    echo "Error: checksum mismatch for $ARCHIVE_NAME" >&2
    echo "  Expected: $EXPECTED" >&2
    echo "  Got:      $GOT" >&2
    exit 1
  fi
fi

# ---- Extract and install ---------------------------------------------------
echo "Extracting..." >&2
tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

BINARY="$TMP_DIR/kei"
if [ ! -f "$BINARY" ]; then
  echo "Error: kei binary not found in archive." >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "$BINARY" "$INSTALL_DIR/kei"

echo "Installed kei $VERSION to $INSTALL_DIR/kei" >&2
echo "Ensure $INSTALL_DIR is on your PATH." >&2

# ---- Verify installation ---------------------------------------------------
if command -v kei &>/dev/null; then
  INSTALLED_VERSION="$(kei --version 2>/dev/null || true)"
  echo "Verified: $INSTALLED_VERSION" >&2
fi
