import json
from pathlib import Path
import tempfile
import unittest

from androidlibboxlite.lockfile import ReleaseLock
from androidlibboxlite.manifest import write_release_metadata
from androidlibboxlite.verify import AbiReport, VerificationReport
from tests.fixtures import release_lock_dict


class ManifestTest(unittest.TestCase):
    def test_writes_canonical_manifest_and_sorted_checksums(self) -> None:
        lock = ReleaseLock.from_json(json.dumps(release_lock_dict()).encode("utf-8"))
        report = VerificationReport(
            abis={
                "arm64-v8a": AbiReport(machine=183, size=3, sha256="1" * 64),
                "armeabi-v7a": AbiReport(machine=40, size=3, sha256="2" * 64),
                "x86": AbiReport(machine=3, size=3, sha256="3" * 64),
                "x86_64": AbiReport(machine=62, size=3, sha256="4" * 64),
            },
            classes=("io/nekohasekai/libbox/Libbox.class", "io/nekohasekai/libbox/SetupOptions.class"),
            sources=("io/nekohasekai/libbox/Libbox.java", "io/nekohasekai/libbox/SetupOptions.java"),
        )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            aar = root / "libbox.aar"
            sources = root / "libbox-sources.jar"
            aar.write_bytes(b"aar")
            sources.write_bytes(b"sources")

            manifest, sums = write_release_metadata(lock, report, aar, sources, root)

            parsed = json.loads(manifest.read_text(encoding="utf-8"))
            self.assertEqual(lock.source.tag, parsed["release"]["tag"])
            self.assertEqual(["libbox-sources.jar", "libbox.aar"], sorted(parsed["artifacts"]))
            lines = sums.read_text(encoding="ascii").splitlines()
            self.assertEqual(
                ["build-manifest.json", "libbox-sources.jar", "libbox.aar"],
                [line.split("  ", 1)[1] for line in lines],
            )


if __name__ == "__main__":
    unittest.main()
