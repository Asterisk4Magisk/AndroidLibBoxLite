import copy
import json
import unittest

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock
from tests.fixtures import release_lock_dict


class ReleaseLockTest(unittest.TestCase):
    def test_round_trip_is_canonical(self) -> None:
        encoded = json.dumps(release_lock_dict()).encode("utf-8")

        lock = ReleaseLock.from_json(encoded)
        canonical = lock.to_canonical_json()

        self.assertEqual(lock, ReleaseLock.from_json(canonical))
        self.assertTrue(canonical.endswith(b"\n"))
        self.assertEqual(canonical, ReleaseLock.from_json(canonical).to_canonical_json())

    def test_rejects_unknown_and_missing_fields(self) -> None:
        for mutate in (
            lambda value: value.update({"unknown": True}),
            lambda value: value["source"].pop("commit"),
            lambda value: value["toolchain"]["go"].update({"channel": "latest"}),
        ):
            value = copy.deepcopy(release_lock_dict())
            mutate(value)
            with self.subTest(value=value):
                with self.assertRaises(ReleaseError) as caught:
                    ReleaseLock.from_json(json.dumps(value).encode("utf-8"))
                self.assertEqual("LOCK_SCHEMA_INVALID", caught.exception.code)

    def test_rejects_forbidden_tags_and_changed_abi_order(self) -> None:
        for field, value in (
            ("tags", ["with_gvisor", "with_embedded_tor"]),
            ("abis", ["x86_64", "x86", "armeabi-v7a", "arm64-v8a"]),
        ):
            payload = release_lock_dict()
            payload["libbox"][field] = value
            with self.subTest(field=field):
                with self.assertRaises(ReleaseError) as caught:
                    ReleaseLock.from_json(json.dumps(payload).encode("utf-8"))
                self.assertEqual("LOCK_POLICY_INVALID", caught.exception.code)


if __name__ == "__main__":
    unittest.main()
