#!/bin/bash
set -euo pipefail

# Fetch the pinned kei-proxy release inputs used by the kei-cli archives.
#
# The shared release contract is currently assumed to publish one archive per
# platform at:
#   kei-proxy/<version>/kei-proxy_<version>_<GOOS>_<GOARCH>.tar.gz
# KEI_PROXY_VERSION may be written with a leading v (for example, v0.1.0)
# for readability; S3 paths and filenames use the unprefixed version.
# Each archive must contain an executable named kei-proxy. Set
# KEI_PROXY_ARTIFACT_TEMPLATE when the proxy release uses another filename;
# the value may contain {version}, {os}, and {arch} placeholders.

PROJECT="kei-proxy"
PIN="${KEI_PROXY_VERSION:?KEI_PROXY_VERSION must be set}"
VERSION="${PIN#v}"
OUTPUT_DIR="${KEI_PROXY_OUTPUT_DIR:-tmp/kei-proxy}"
TEMPLATE="${KEI_PROXY_ARTIFACT_TEMPLATE:-kei-proxy_{version}_{os}_{arch}.tar.gz}"

if [ -z "${AWS_S3_RELEASES_BUCKET:-}" ] && [ -z "${AWS_S3_RELEASES_URL_BASE:-}" ]; then
  echo "Error: set AWS_S3_RELEASES_BUCKET or AWS_S3_RELEASES_URL_BASE to fetch kei-proxy." >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

for os in darwin linux; do
  for arch in amd64 arm64; do
    artifact="$TEMPLATE"
    artifact="${artifact//\{version\}/$VERSION}"
    artifact="${artifact//\{os\}/$os}"
    artifact="${artifact//\{arch\}/$arch}"
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
