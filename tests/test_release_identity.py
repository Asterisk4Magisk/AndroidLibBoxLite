import json
from pathlib import Path
import tempfile
import unittest

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock
from androidlibboxlite.release import validate_release_identity
from androidlibboxlite.upstream import BASELINE_COMMIT, BASELINE_TAG
from tests.fixtures import release_lock_dict


class ReleaseIdentityTest(unittest.TestCase):
    def test_accepts_canonical_lock_and_matching_upstream(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            value = release_lock_dict()
            path = root / f"{BASELINE_TAG}.json"
            path.write_bytes(ReleaseLock.from_json(json.dumps(value).encode("utf-8")).to_canonical_json())

            identity = validate_release_identity(
                BASELINE_TAG,
                root,
                BASELINE_COMMIT,
            )

            self.assertEqual(path.resolve(), identity.path)
            self.assertTrue(identity.prerelease)

    def test_rejects_release_before_current_baseline(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            value = release_lock_dict()
            source = value["source"]
            libbox = value["libbox"]
            assert isinstance(source, dict)
            assert isinstance(libbox, dict)
            source["tag"] = "v1.14.0-alpha.47"
            source["commit"] = "37b4386bddb143e0780435c467cd2c5f1250a4ff"
            libbox["ldflags"] = (
                "-X github.com/sagernet/sing-box/constant.Version=1.14.0-alpha.47 "
                "-X internal/godebug.defaultGODEBUG=multipathtcp=0 "
                "-checklinkname=0 -s -w -buildid="
            )
            path = root / "v1.14.0-alpha.47.json"
            path.write_bytes(
                ReleaseLock.from_json(json.dumps(value).encode("utf-8")).to_canonical_json()
            )

            with self.assertRaises(ReleaseError) as caught:
                validate_release_identity(
                    "v1.14.0-alpha.47",
                    root,
                    "37b4386bddb143e0780435c467cd2c5f1250a4ff",
                )

            self.assertEqual("RELEASE_IDENTITY_INVALID", caught.exception.code)

    def test_rejects_path_input_and_moved_tag(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for tag, commit in (("../escape", "a" * 40), (BASELINE_TAG, "a" * 40)):
                if tag.startswith("v"):
                    (root / f"{tag}.json").write_bytes(
                        ReleaseLock.from_json(json.dumps(release_lock_dict()).encode("utf-8")).to_canonical_json()
                    )
                with self.subTest(tag=tag), self.assertRaises(ReleaseError):
                    validate_release_identity(tag, root, commit)


if __name__ == "__main__":
    unittest.main()
