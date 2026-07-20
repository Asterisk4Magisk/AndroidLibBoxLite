from __future__ import annotations

from dataclasses import dataclass
import json
import os
import re
from typing import Callable, Iterator, Mapping
import urllib.error
import urllib.request
from urllib.parse import urlparse
from datetime import datetime, timezone

from .errors import ReleaseError
from .semver import GitTag


_REPOSITORY_PART = re.compile(r"^[A-Za-z0-9_.-]{1,100}$")
_MAX_RESPONSE_BYTES = 8 * 1024 * 1024


@dataclass(frozen=True)
class HttpResponse:
    status: int
    headers: Mapping[str, str]
    body: bytes


Transport = Callable[[str, dict[str, str]], HttpResponse]


def _default_transport(url: str, headers: dict[str, str]) -> HttpResponse:
    request = urllib.request.Request(url, headers=headers, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read(_MAX_RESPONSE_BYTES + 1)
            return HttpResponse(
                status=response.status,
                headers={key.lower(): value for key, value in response.headers.items()},
                body=body,
            )
    except urllib.error.URLError as failure:
        raise ReleaseError("GITHUB_REQUEST_FAILED", str(failure)) from failure


class GitHubClient:
    def __init__(self, transport: Transport | None = None, token: str | None = None) -> None:
        self._transport = transport or _default_transport
        self._token = token if token is not None else os.environ.get("GITHUB_TOKEN")

    def iter_tags(self, owner: str, repo: str) -> Iterator[GitTag]:
        url = self._repo_url(owner, repo, "tags?per_page=100")
        for payload in self._iter_pages(url):
            if not isinstance(payload, list):
                raise ReleaseError("GITHUB_RESPONSE_INVALID", "tag response is not an array")
            for item in payload:
                try:
                    name = item["name"]
                    commit = item["commit"]["sha"]
                except (KeyError, TypeError) as failure:
                    raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid tag object") from failure
                if not isinstance(name, str) or not isinstance(commit, str):
                    raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid tag fields")
                yield GitTag(name=name, commit=commit.lower())

    def published_release_tags(self, owner: str, repo: str) -> set[str]:
        url = self._repo_url(owner, repo, "releases?per_page=100")
        tags: set[str] = set()
        for payload in self._iter_pages(url):
            if not isinstance(payload, list):
                raise ReleaseError("GITHUB_RESPONSE_INVALID", "release response is not an array")
            for item in payload:
                if not isinstance(item, dict):
                    raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid release object")
                tag = item.get("tag_name")
                draft = item.get("draft")
                if not isinstance(tag, str) or not isinstance(draft, bool):
                    raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid release fields")
                if not draft:
                    tags.add(tag)
        return tags

    def commit_timestamp(self, owner: str, repo: str, commit: str) -> int:
        if re.fullmatch(r"[0-9a-f]{40}", commit) is None:
            raise ReleaseError("UPSTREAM_COMMIT_INVALID", f"invalid commit: {commit!r}")
        payloads = list(self._iter_pages(self._repo_url(owner, repo, f"git/commits/{commit}")))
        if len(payloads) != 1 or not isinstance(payloads[0], dict):
            raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid commit response")
        try:
            raw = payloads[0]["committer"]["date"]
            if not isinstance(raw, str):
                raise TypeError
            parsed = datetime.fromisoformat(raw.replace("Z", "+00:00"))
            if parsed.tzinfo is None:
                raise ValueError
            return int(parsed.astimezone(timezone.utc).timestamp())
        except (KeyError, TypeError, ValueError) as failure:
            raise ReleaseError("GITHUB_RESPONSE_INVALID", "invalid commit timestamp") from failure

    def _repo_url(self, owner: str, repo: str, suffix: str) -> str:
        if _REPOSITORY_PART.fullmatch(owner) is None or _REPOSITORY_PART.fullmatch(repo) is None:
            raise ReleaseError("GITHUB_REPOSITORY_INVALID", f"invalid repository: {owner}/{repo}")
        return f"https://api.github.com/repos/{owner}/{repo}/{suffix}"

    def _iter_pages(self, initial_url: str) -> Iterator[object]:
        url: str | None = initial_url
        visited: set[str] = set()
        while url is not None:
            self._require_api_url(url)
            if url in visited:
                raise ReleaseError("GITHUB_PAGINATION_INVALID", "pagination cycle detected")
            visited.add(url)
            headers = {
                "accept": "application/vnd.github+json",
                "user-agent": "AndroidLibBoxLite/0.1",
                "x-github-api-version": "2022-11-28",
            }
            if self._token:
                headers["authorization"] = f"Bearer {self._token}"
            response = self._transport(url, headers)
            if response.status != 200:
                raise ReleaseError("GITHUB_REQUEST_FAILED", f"HTTP {response.status} for {url}")
            if len(response.body) > _MAX_RESPONSE_BYTES:
                raise ReleaseError("GITHUB_RESPONSE_INVALID", "response exceeds size limit")
            try:
                yield json.loads(response.body.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError) as failure:
                raise ReleaseError("GITHUB_RESPONSE_INVALID", "response is not UTF-8 JSON") from failure
            link = next(
                (value for key, value in response.headers.items() if key.lower() == "link"),
                None,
            )
            url = self._next_link(link)

    @staticmethod
    def _next_link(header: str | None) -> str | None:
        if header is None:
            return None
        for part in header.split(","):
            sections = [section.strip() for section in part.split(";")]
            if len(sections) >= 2 and 'rel="next"' in sections[1:]:
                target = sections[0]
                if not target.startswith("<") or not target.endswith(">"):
                    raise ReleaseError("GITHUB_PAGINATION_INVALID", "malformed next link")
                url = target[1:-1]
                GitHubClient._require_api_url(url)
                return url
        return None

    @staticmethod
    def _require_api_url(url: str) -> None:
        parsed = urlparse(url)
        if parsed.scheme != "https" or parsed.hostname != "api.github.com" or parsed.username:
            raise ReleaseError("GITHUB_PAGINATION_INVALID", f"untrusted GitHub API URL: {url}")
