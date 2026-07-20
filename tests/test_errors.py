import unittest

from androidlibboxlite.errors import ReleaseError


class ReleaseErrorTest(unittest.TestCase):
    def test_formats_stable_code(self) -> None:
        error = ReleaseError("LOCK_SCHEMA_INVALID", "missing schema")

        self.assertEqual("LOCK_SCHEMA_INVALID: missing schema", str(error))
        self.assertEqual("LOCK_SCHEMA_INVALID", error.code)


if __name__ == "__main__":
    unittest.main()
