#!/bin/bash
set -euo pipefail

# install.sh — Download and install the kei CLI from AWS S3.
#
# The script detects the OS and architecture, downloads the matching
# release archive from S3, verifies its SHA-256 checksum, and installs
# the kei binary to the target directory.
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
# Required configuration (set as environment variables before running):
#
#   AWS_S3_RELEASES_BUCKET   — S3 bucket name (e.g. "kei-releases")
#   AWS_S3_RELEASES_REGION   — AWS region (e.g. "us-east-1")
#   AWS_S3_RELEASES_URL_BASE — Public S3 endpoint for curl downloads.
#                              Format:
#                                https://<bucket>.s3.<region>.amazonaws.com
#                              or a CloudFront distribution URL.
#
# The install URL is constructed as:
#   $AWS_S3_RELEASES_URL_BASE/kei-cli/<version>/<artifact>
#
# If the S3 bucket is private (not publicly readable), the download will
# fail. In that case, either make the bucket public for GETs, or place a
# CloudFront/CDN distribution in front of it and set
# AWS_S3_RELEASES_URL_BASE to the distribution URL.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

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

# ---- Required configuration ------------------------------------------------
if [ -z "${AWS_S3_RELEASES_URL_BASE:-}" ]; then
  echo "Error: AWS_S3_RELEASES_URL_BASE is not set." >&2
  echo "Set it to the public S3 endpoint or CloudFront URL for kei releases." >&2
  echo "Example: export AWS_S3_RELEASES_URL_BASE='https://kei-releases.s3.us-east-1.amazonaws.com'" >&2
  exit 1
fi

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
ARCHIVE_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$ARCHIVE_NAME"
CHECKSUM_URL="$AWS_S3_RELEASES_URL_BASE/$PROJECT/$VERSION/$CHECKSUM_NAME"

# ---- Temporary directory ---------------------------------------------------
if [ -z "${TMP_DIR:-}" ]; then
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
else
  mkdir -p "$TMP_DIR"
fi

# ---- Download and verify ---------------------------------------------------
echo "Downloading $ARCHIVE_NAME..." >&2
curl -fsSL --connect-timeout 15 --retry 3 "$ARCHIVE_URL" -o "$TMP_DIR/$ARCHIVE_NAME"

echo "Downloading checksums..." >&2
curl -fsSL --connect-timeout 15 --retry 3 "$CHECKSUM_URL" -o "$TMP_DIR/$CHECKSUM_NAME"

echo "Verifying checksum..." >&2
(cd "$TMP_DIR" && shasum -a 256 -c "$CHECKSUM_NAME" --ignore-missing 2>/dev/null) || {
  # Fallback: manual verification
  EXPECTED="$(grep "$ARCHIVE_NAME" "$TMP_DIR/$CHECKSUM_NAME" | awk '{print $1}')"
  if [ -z "$EXPECTED" ]; then
    echo "Error: $ARCHIVE_NAME not found in checksums file." >&2
    exit 1
  fi
  GOT="$(shasum -a 256 "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
  if [ "$EXPECTED" != "$GOT" ]; then
    echo "Error: checksum mismatch for $ARCHIVE_NAME" >&2
    echo "  Expected: $EXPECTED" >&2
    echo "  Got:      $GOT" >&2
    exit 1
  fi
}

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
