import hashlib
from pathlib import Path
import tempfile
import unittest

from publish import check_existing, release_version, verify_files


class PublicationValidationTests(unittest.TestCase):
    def test_stable_tags_only(self):
        self.assertEqual(release_version("v0.1.0"), "0.1.0")
        for tag in ("main", "v0.1.0-rc1", "v01.2.3", "--help", "v1.2.3\n"):
            with self.assertRaises(ValueError):
                release_version(tag)

    def test_all_four_packages_must_match(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            files = [root / f"mailfire_0.1.0_linux_{arch}.{fmt}"
                     for arch in ("amd64", "arm64") for fmt in ("deb", "rpm")]
            for file in files:
                file.write_bytes(file.name.encode())
            (root / "checksums.txt").write_text("\n".join(
                f"{hashlib.sha256(f.read_bytes()).hexdigest()}  {f.name}" for f in files
            ))
            self.assertEqual(len(verify_files(root, "0.1.0")), 4)
            files[-1].write_bytes(b"changed")
            with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
                verify_files(root, "0.1.0")

    def test_retry_requires_matching_input_provenance(self):
        package = {"filename": "package.rpm", "tags": {"info": ["github-sha256-abc"]}}
        check_existing(package, "abc")
        with self.assertRaises(ValueError):
            check_existing(package, "def")
        with self.assertRaises(ValueError):
            check_existing({"filename": "unknown.rpm"}, "abc")


if __name__ == "__main__":
    unittest.main()
