#!/bin/bash
set -euo pipefail

# Fetch the pinned kei-proxy release inputs used by the kei-cli archives.
#
# The shared release contract is currently assumed to publish one archive per
# platform at:
#   kei-proxy/<version>/kei-proxy_<version>_<OS>_<ARCH>.tar.gz
# where OS is macOS/Linux and ARCH is x86_64/arm64, matching the CLI archive
# naming convention.
# KEI_PROXY_VERSION may be written with a leading v (for example, v0.1.0)
# for readability; S3 paths and filenames use the unprefixed version.
# Each archive must contain an executable named kei-proxy. The filename is
# derived from the release contract below so a malformed environment value
# cannot append a second platform suffix or an unmatched template delimiter.
#
# The version is read from the KEI_PROXY_VERSION file at the repo root.
# Override with the KEI_PROXY_VERSION environment variable if needed.

PROJECT="kei-proxy"

if [ -n "${KEI_PROXY_VERSION:-}" ]; then
  PIN="$KEI_PROXY_VERSION"
else
  PIN_FILE="KEI_PROXY_VERSION"
  if [ ! -f "$PIN_FILE" ]; then
    echo "Error: KEI_PROXY_VERSION not set and $PIN_FILE not found at repo root." >&2
    exit 1
  fi
  PIN=$(cat "$PIN_FILE" | tr -d '[:space:]')
fi

if ! echo "$PIN" | grep -qE '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo "Error: KEI_PROXY_VERSION '$PIN' is not a valid semver tag (vX.Y.Z)." >&2
  exit 1
fi

VERSION="${PIN#v}"
OUTPUT_DIR="${KEI_PROXY_OUTPUT_DIR:-tmp/kei-proxy}"

if [ -z "${AWS_S3_RELEASES_BUCKET:-}" ] && [ -z "${AWS_S3_RELEASES_URL_BASE:-}" ]; then
  echo "Error: set AWS_S3_RELEASES_BUCKET or AWS_S3_RELEASES_URL_BASE to fetch kei-proxy." >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

for os in darwin linux; do
  for arch in amd64 arm64; do
    case "$os" in
      darwin) archive_os="macOS" ;;
      linux) archive_os="Linux" ;;
    esac
    case "$arch" in
      amd64) archive_arch="x86_64" ;;
      arm64) archive_arch="arm64" ;;
    esac

    artifact="kei-proxy_${VERSION}_${archive_os}_${archive_arch}.tar.gz"
    destination="$OUTPUT_DIR/$os/$arch"
    archive="$destination/$artifact"
    mkdir -p "$destination"

    if [ -n "${AWS_S3_RELEASES_BUCKET:-}" ]; then
      aws s3 cp \
        "s3://${AWS_S3_RELEASES_BUCKET}/${PROJECT}/${VERSION}/${artifact}" \
        "$archive" \
        --region "${AWS_S3_RELEASES_REGION:-${AWS_REGION:-us-east-1}}"
    else
      curl -fsSL --connect-timeout 15 --retry 3 \
        "${AWS_S3_RELEASES_URL_BASE%/}/${PROJECT}/${VERSION}/${artifact}" \
        -o "$archive"
    fi

    tar -xzf "$archive" -C "$destination"
    proxy="$destination/kei-proxy"
    if [ ! -f "$proxy" ]; then
      echo "Error: $artifact does not contain a kei-proxy binary." >&2
      exit 1
    fi
    chmod 0755 "$proxy"
    rm -f "$archive"
  done
done
