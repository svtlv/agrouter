#!/usr/bin/env python3
"""Exercise release selection and checksum enforcement of install.sh without network access."""
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


INSTALLER = Path(__file__).with_name("install.sh")
TAG = "v1.2.3"
BINARY = b"#!/bin/sh\necho fixture\n"


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "tools"
        self.bin.mkdir()
        self.destination = self.root / "install with spaces"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        AGROUTER_INSTALL_DIR=str(self.destination),
                        TEST_FIXTURES=str(self.root), TEST_OS="Linux", TEST_ARCH="x86_64")
        self.env.pop("AGROUTER_VERSION", None)
        self.write_tool("uname", '#!/bin/sh\ncase "$1" in -s) echo "$TEST_OS";; -m) echo "$TEST_ARCH";; esac\n')
        # serves the last path segment of every URL from the fixture directory
        self.write_tool("curl", '''#!/usr/bin/env python3
import os, pathlib, sys
args = sys.argv[1:]
url = next(arg for arg in args if arg.startswith("https://"))
source = pathlib.Path(os.environ["TEST_FIXTURES"]) / url.rsplit("/", 1)[1]
if not source.exists():
    sys.exit(22)
if "-o" in args:
    pathlib.Path(args[args.index("-o") + 1]).write_bytes(source.read_bytes())
else:
    sys.stdout.buffer.write(source.read_bytes())
''')
        (self.root / "latest").write_text(json.dumps({"tag_name": TAG}))

    def write_tool(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def fixture(self, os_name="linux", arch="amd64", binary=True, tag=TAG):
        asset = f"agrouter_{tag[1:]}_{os_name}_{arch}.tar.gz"
        path = self.root / asset
        with tarfile.open(path, "w:gz") as archive:
            member = tarfile.TarInfo("agrouter" if binary else "other")
            member.size = len(BINARY)
            archive.addfile(member, io.BytesIO(BINARY))
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        (self.root / "checksums.txt").write_text(f"{digest}  {asset}\n")
        return path

    def run_installer(self):
        return subprocess.run(["sh", str(INSTALLER)], env=self.env,
                              capture_output=True, text=True)

    def test_latest_release_all_platforms(self):
        for system, machine, release_os, arch in [
            ("Linux", "x86_64", "linux", "amd64"),
            ("Linux", "aarch64", "linux", "arm64"),
            ("Darwin", "x86_64", "darwin", "amd64"),
            ("Darwin", "arm64", "darwin", "arm64"),
        ]:
            with self.subTest(system=system, machine=machine):
                self.env.update(TEST_OS=system, TEST_ARCH=machine)
                self.fixture(release_os, arch)
                result = self.run_installer()
                self.assertEqual(result.returncode, 0, result.stderr)
                installed = self.destination / "agrouter"
                self.assertEqual(installed.read_bytes(), BINARY)
                self.assertEqual(installed.stat().st_mode & 0o777, 0o755)

    def test_pinned_release_skips_latest_lookup(self):
        self.fixture(tag="v0.9.0-rc.1")
        self.env["AGROUTER_VERSION"] = "v0.9.0-rc.1"
        (self.root / "latest").unlink()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Installed v0.9.0-rc.1", result.stdout)

    def test_checksum_mismatch_preserves_existing_installation(self):
        archive = self.fixture()
        archive.write_bytes(archive.read_bytes() + b"tampered")
        self.destination.mkdir()
        installed = self.destination / "agrouter"
        installed.write_text("previous version")
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertEqual(installed.read_text(), "previous version")

    def test_missing_checksum_rejected(self):
        self.fixture()
        (self.root / "checksums.txt").write_text("")
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing or invalid checksum", result.stderr)
        self.assertFalse(self.destination.exists())

    def test_missing_binary_rejected(self):
        self.fixture(binary=False)
        self.assertNotEqual(self.run_installer().returncode, 0)
        self.assertFalse(self.destination.exists())

    def test_invalid_release_rejected(self):
        for tag in ["v2.0.0", "1.0.0", "v1.0", "v1.0.0-RC1", ""]:
            with self.subTest(tag=tag):
                self.env["AGROUTER_VERSION"] = tag
                if not tag:
                    (self.root / "latest").write_text(json.dumps({"message": "Not Found"}))
                result = self.run_installer()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("invalid release tag", result.stderr)

    def test_unsupported_platform_rejected(self):
        for key, value, message in [
            ("TEST_ARCH", "riscv64", "unsupported architecture"),
            ("TEST_OS", "FreeBSD", "unsupported OS"),
        ]:
            with self.subTest(value=value):
                env = dict(self.env, **{key: value})
                result = subprocess.run(["sh", str(INSTALLER)], env=env,
                                        capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(message, result.stderr)


if __name__ == "__main__":
    unittest.main()
