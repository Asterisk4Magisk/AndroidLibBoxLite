import json
from pathlib import Path
import unittest

from androidlibboxlite.upstream import (
    BASELINE_COMMIT,
    BASELINE_TAG,
    UPSTREAM_REPOSITORY,
    source_archive_url,
)


class UpstreamTest(unittest.TestCase):
    def test_exposes_reviewed_source_policy(self) -> None:
        self.assertEqual("reF1nd/sing-box", UPSTREAM_REPOSITORY)
        self.assertEqual(
            "https://codeload.github.com/reF1nd/sing-box/zip/" + "a" * 40,
            source_archive_url("a" * 40),
        )
        baseline = json.loads(
            (Path(__file__).parents[1] / "config" / "baseline.json").read_text()
        )
        self.assertEqual(
            {
                "commit": "b4de7f7013014b87cff5ae2c21952d9d9127c5d2",
                "tag": "v1.14.0-beta.5-reF1nd",
            },
            baseline,
        )
        self.assertEqual(baseline["commit"], BASELINE_COMMIT)
        self.assertEqual(baseline["tag"], BASELINE_TAG)


if __name__ == "__main__":
    unittest.main()
