import json
import unittest

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.github_api import GitHubClient, HttpResponse


class FakeTransport:
    def __init__(self, responses: dict[str, HttpResponse]) -> None:
        self.responses = responses
        self.urls: list[str] = []

    def __call__(self, url: str, headers: dict[str, str]) -> HttpResponse:
        self.urls.append(url)
        return self.responses[url]


def response(payload: object, link: str | None = None) -> HttpResponse:
    headers = {"content-type": "application/json"}
    if link is not None:
        headers["link"] = link
    return HttpResponse(200, headers, json.dumps(payload).encode("utf-8"))


class GitHubClientTest(unittest.TestCase):
    def test_reads_every_tag_page_and_release(self) -> None:
        first = "https://api.github.com/repos/SagerNet/sing-box/tags?per_page=100"
        second = first + "&page=2"
        releases = "https://api.github.com/repos/Asterisk4Magisk/AndroidLibBoxLite/releases?per_page=100"
        transport = FakeTransport(
            {
                first: response(
                    [{"name": "v1.14.0-alpha.47", "commit": {"sha": "a" * 40}}],
                    f'<{second}>; rel="next"',
                ),
                second: response([{"name": "v1.14.0-alpha.48", "commit": {"sha": "b" * 40}}]),
                releases: response(
                    [
                        {"tag_name": "v1.14.0-alpha.47", "draft": False},
                        {"tag_name": "ignored-draft", "draft": True},
                    ]
                ),
            }
        )
        client = GitHubClient(transport=transport)

        self.assertEqual(
            ["v1.14.0-alpha.47", "v1.14.0-alpha.48"],
            [item.name for item in client.iter_tags("SagerNet", "sing-box")],
        )
        self.assertEqual(
            {"v1.14.0-alpha.47"},
            client.published_release_tags("Asterisk4Magisk", "AndroidLibBoxLite"),
        )

    def test_rejects_untrusted_pagination_host(self) -> None:
        first = "https://api.github.com/repos/SagerNet/sing-box/tags?per_page=100"
        transport = FakeTransport(
            {
                first: response([], '<https://example.invalid/steal>; rel="next"'),
            }
        )

        with self.assertRaises(ReleaseError) as caught:
            list(GitHubClient(transport=transport).iter_tags("SagerNet", "sing-box"))

        self.assertEqual("GITHUB_PAGINATION_INVALID", caught.exception.code)

    def test_reads_commit_timestamp_as_utc_epoch(self) -> None:
        commit = "a" * 40
        url = f"https://api.github.com/repos/SagerNet/sing-box/git/commits/{commit}"
        client = GitHubClient(
            transport=FakeTransport(
                {url: response({"committer": {"date": "2026-07-20T00:00:00Z"}})}
            )
        )

        self.assertEqual(1784505600, client.commit_timestamp("SagerNet", "sing-box", commit))


if __name__ == "__main__":
    unittest.main()
