import unittest

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.semver import GitTag, SemVer, discover_unreleased


class SemVerTest(unittest.TestCase):
    def test_orders_prereleases_before_stable(self) -> None:
        tags = [
            SemVer.parse("v1.14.0"),
            SemVer.parse("v1.14.0-rc.1"),
            SemVer.parse("v1.14.0-beta.2"),
            SemVer.parse("v1.14.0-alpha.48"),
        ]

        self.assertEqual(
            [
                "v1.14.0-alpha.48",
                "v1.14.0-beta.2",
                "v1.14.0-rc.1",
                "v1.14.0",
            ],
            [item.tag for item in sorted(tags)],
        )

    def test_orders_ref1nd_variants_by_semantic_version(self) -> None:
        tags = [
            SemVer.parse("v1.14.0-reF1nd"),
            SemVer.parse("v1.14.0-rc.1-reF1nd"),
            SemVer.parse("v1.14.0-beta.10-reF1nd"),
            SemVer.parse("v1.14.0-beta.9-reF1nd"),
        ]

        self.assertEqual(
            [
                "v1.14.0-beta.9-reF1nd",
                "v1.14.0-beta.10-reF1nd",
                "v1.14.0-rc.1-reF1nd",
                "v1.14.0-reF1nd",
            ],
            [item.tag for item in sorted(tags)],
        )

    def test_treats_ref1nd_stable_variant_as_stable(self) -> None:
        self.assertFalse(SemVer.parse("v1.14.0-reF1nd").is_prerelease)

    def test_rejects_noncanonical_tags(self) -> None:
        for value in ("1.14.0", "v01.14.0", "v1.14", "v1.14.0-alpha..1", "v1.14.0+meta"):
            with self.subTest(value=value):
                with self.assertRaises(ReleaseError) as caught:
                    SemVer.parse(value)
                self.assertEqual("UPSTREAM_TAG_INVALID", caught.exception.code)

    def test_discovers_every_unreleased_tag_after_baseline(self) -> None:
        tags = [
            GitTag("v1.14.0", "c" * 40),
            GitTag("not-a-release", "d" * 40),
            GitTag("v1.14.0-beta.10-reF1nd", "1" * 40),
            GitTag("v1.14.0-beta.6-reF1nd", "b" * 40),
            GitTag("v1.14.0-beta.5-reF1nd", "a" * 40),
            GitTag("v1.14.0-beta.4-reF1nd", "f" * 40),
            GitTag("v1.13.9", "e" * 40),
        ]

        result = discover_unreleased(
            tags,
            {"v1.14.0-beta.5-reF1nd"},
            SemVer.parse("v1.14.0-beta.5-reF1nd"),
        )

        self.assertEqual(
            [
                "v1.14.0-beta.6-reF1nd",
                "v1.14.0-beta.10-reF1nd",
                "v1.14.0",
            ],
            [item.name for item in result],
        )

    def test_rejects_conflicting_duplicate_tag_objects(self) -> None:
        with self.assertRaises(ReleaseError) as caught:
            discover_unreleased(
                [GitTag("v1.14.0", "a" * 40), GitTag("v1.14.0", "b" * 40)],
                set(),
                SemVer.parse("v1.14.0-alpha.47"),
            )

        self.assertEqual("UPSTREAM_TAG_MOVED", caught.exception.code)


if __name__ == "__main__":
    unittest.main()
