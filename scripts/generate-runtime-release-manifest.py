#!/usr/bin/env python3
"""Generate a client-origin-independent manifest for a kei-cli runtime release."""

import argparse
import hashlib
import json
import re
from pathlib import Path

PLATFORMS = (
    ("darwin", "arm64", "macOS_arm64"),
    ("darwin", "amd64", "macOS_x86_64"),
    ("linux", "arm64", "Linux_arm64"),
    ("linux", "amd64", "Linux_x86_64"),
)
VERSION_RE = re.compile(r"^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9._-]+)?$")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")


def version(value: str, label: str) -> str:
    if not VERSION_RE.fullmatch(value):
        raise ValueError(f"{label} must be a semantic version: {value!r}")
    return value.removeprefix("v")


def generate(release_id: str, cli_version: str, proxy_version: str,
             artifact_dir: Path, checksums_file: Path) -> dict:
    if not VERSION_RE.fullmatch(release_id) or not release_id.startswith("v"):
        raise ValueError("release identifier must be a v-prefixed semantic version")
    cli = version(cli_version, "kei-cli version")
    proxy = version(proxy_version, "kei-proxy version")
    if cli != release_id[1:]:
        raise ValueError("kei-cli version must match the approved release identifier")

    checksums = {}
    for line in checksums_file.read_text(encoding="utf-8").splitlines():
        parts = line.split()
        if len(parts) != 2:
            continue
        digest, filename = parts
        filename = filename.removeprefix("*")
        if SHA256_RE.fullmatch(digest):
            if filename in checksums:
                raise ValueError(f"duplicate checksum entry for {filename}")
            checksums[filename] = digest.lower()

    artifacts = []
    for goos, goarch, label in PLATFORMS:
        filename = f"kei-cli_{cli}_{label}.tar.gz"
        archive = artifact_dir / filename
        if not archive.is_file():
            raise ValueError(f"missing platform artifact: {filename}")
        expected = checksums.get(filename)
        if expected is None:
            raise ValueError(f"checksums file is missing {filename}")
        actual = hashlib.sha256(archive.read_bytes()).hexdigest()
        if actual != expected:
            raise ValueError(f"checksum mismatch for {filename}")
        artifacts.append({
            "os": goos,
            "architecture": goarch,
            "path": f"kei-cli/{cli}/{filename}",
            "sha256": expected,
        })

    return {
        "schema": "kei.runtime-release-manifest/v1",
        "release_id": release_id,
        "components": {"kei": cli, "kei-proxy": proxy},
        "artifacts": artifacts,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-id", required=True)
    parser.add_argument("--cli-version", required=True)
    parser.add_argument("--proxy-version", required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--checksums", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    manifest = generate(args.release_id, args.cli_version, args.proxy_version,
                        args.artifact_dir, args.checksums)
    args.output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as exc:
        raise SystemExit(f"release manifest: {exc}") from exc
