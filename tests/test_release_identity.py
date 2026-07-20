import json
from pathlib import Path
import tempfile
import unittest

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock
from androidlibboxlite.release import validate_release_identity
from tests.fixtures import release_lock_dict


class ReleaseIdentityTest(unittest.TestCase):
    def test_accepts_canonical_lock_and_matching_upstream(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            value = release_lock_dict()
            path = root / "v1.14.0-alpha.47.json"
            path.write_bytes(ReleaseLock.from_json(json.dumps(value).encode("utf-8")).to_canonical_json())

            identity = validate_release_identity(
                "v1.14.0-alpha.47",
                root,
                "37b4386bddb143e0780435c467cd2c5f1250a4ff",
            )

            self.assertEqual(path.resolve(), identity.path)
            self.assertTrue(identity.prerelease)

    def test_rejects_path_input_and_moved_tag(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for tag, commit in (("../escape", "a" * 40), ("v1.14.0-alpha.47", "a" * 40)):
                if tag.startswith("v"):
                    (root / f"{tag}.json").write_bytes(
                        ReleaseLock.from_json(json.dumps(release_lock_dict()).encode("utf-8")).to_canonical_json()
                    )
                with self.subTest(tag=tag), self.assertRaises(ReleaseError):
                    validate_release_identity(tag, root, commit)


if __name__ == "__main__":
    unittest.main()
