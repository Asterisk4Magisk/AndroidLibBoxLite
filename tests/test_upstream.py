from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from androidlibboxlite.semver import GitTag
from androidlibboxlite.upstream import (
    BASELINE_COMMIT,
    BASELINE_TAG,
    UPSTREAM_REPOSITORY,
    source_archive_url,
)
from scripts import discover_upstream


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

    def test_discovery_accepts_only_ref1nd_tags(self) -> None:
        class FakeGitHubClient:
            def iter_tags(self, owner: str, repo: str) -> list[GitTag]:
                return [
                    GitTag("v1.14.0-beta.8", "8" * 40),
                    GitTag("v1.14.0-beta.10-reF1nd", "1" * 40),
                ]

            def published_release_tags(self, owner: str, repo: str) -> set[str]:
                return set()

        output = io.StringIO()
        with (
            patch.object(discover_upstream, "GitHubClient", return_value=FakeGitHubClient()),
            patch("sys.argv", ["discover_upstream.py"]),
            redirect_stdout(output),
        ):
            self.assertEqual(0, discover_upstream.main())

        self.assertEqual(
            [
                {
                    "tag": "v1.14.0-beta.10-reF1nd",
                    "commit": "1" * 40,
                }
            ],
            json.loads(output.getvalue()),
        )


if __name__ == "__main__":
    unittest.main()
