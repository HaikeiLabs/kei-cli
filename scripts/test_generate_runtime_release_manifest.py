import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("generate-runtime-release-manifest.py")
SPEC = importlib.util.spec_from_file_location("runtime_release_manifest", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RuntimeReleaseManifestTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.archive_dir = self.root / "dist"
        self.archive_dir.mkdir()
        self.checksums = self.root / "checksums.txt"
        lines = []
        for _, _, label in MODULE.PLATFORMS:
            filename = f"kei-cli_1.2.3_{label}.tar.gz"
            data = f"test artifact: {label}".encode()
            (self.archive_dir / filename).write_bytes(data)
            lines.append(f"{hashlib.sha256(data).hexdigest()}  {filename}")
        self.checksums.write_text("\n".join(lines) + "\n")

    def generate(self, **overrides):
        args = {
            "release_id": "v1.2.3",
            "cli_version": "1.2.3",
            "proxy_version": "v0.9.1",
            "artifact_dir": self.archive_dir,
            "checksums_file": self.checksums,
        }
        args.update(overrides)
        return MODULE.generate(**args)

    def test_manifest_has_components_platforms_checksums_and_relative_paths(self):
        manifest = self.generate()
        self.assertEqual(manifest["schema"], "kei.runtime-release-manifest/v1")
        self.assertEqual(manifest["release_id"], "v1.2.3")
        self.assertEqual(manifest["components"], {"kei": "1.2.3", "kei-proxy": "0.9.1"})
        self.assertEqual(len(manifest["artifacts"]), 4)
        for artifact in manifest["artifacts"]:
            self.assertRegex(artifact["sha256"], r"^[0-9a-f]{64}$")
            self.assertTrue(artifact["path"].startswith("kei-cli/1.2.3/"))
            self.assertNotRegex(artifact["path"], r"^https?://")

    def test_release_identifier_must_match_cli_version(self):
        with self.assertRaisesRegex(ValueError, "must match"):
            self.generate(cli_version="1.2.4")

    def test_every_platform_checksum_is_required_and_verified(self):
        self.checksums.write_text("")
        with self.assertRaisesRegex(ValueError, "missing"):
            self.generate()
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.checksums.write_text("0" * 64 + "  kei-cli_1.2.3_macOS_arm64.tar.gz\n")
            self.generate()

    def test_rejects_unapproved_identifier_shape(self):
        with self.assertRaisesRegex(ValueError, "v-prefixed semantic version"):
            self.generate(release_id="latest")

    def test_output_is_versioned_json(self):
        output = self.root / "manifest.json"
        manifest = self.generate()
        output.write_text(json.dumps(manifest))
        self.assertEqual(json.loads(output.read_text()), manifest)


if __name__ == "__main__":
    unittest.main()
