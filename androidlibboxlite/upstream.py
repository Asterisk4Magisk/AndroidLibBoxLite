from __future__ import annotations


UPSTREAM_OWNER = "reF1nd"
UPSTREAM_NAME = "sing-box"
UPSTREAM_REPOSITORY = f"{UPSTREAM_OWNER}/{UPSTREAM_NAME}"
UPSTREAM_TAG_SUFFIX = "-reF1nd"

BASELINE_TAG = "v1.14.0-beta.5-reF1nd"
BASELINE_COMMIT = "b4de7f7013014b87cff5ae2c21952d9d9127c5d2"


def source_archive_url(commit: str) -> str:
    return f"https://codeload.github.com/{UPSTREAM_REPOSITORY}/zip/{commit}"
