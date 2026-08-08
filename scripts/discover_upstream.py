#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.github_api import GitHubClient
from androidlibboxlite.semver import SemVer, discover_unreleased
from androidlibboxlite.upstream import (
    BASELINE_TAG,
    UPSTREAM_NAME,
    UPSTREAM_OWNER,
    UPSTREAM_TAG_SUFFIX,
)


def main() -> int:
    parser = argparse.ArgumentParser(description="发现尚未发布的 sing-box 版本")
    parser.add_argument("--baseline", default=BASELINE_TAG)
    parser.add_argument("--provider-owner", default="Asterisk4Magisk")
    parser.add_argument("--provider-repo", default="AndroidLibBoxLite")
    parser.add_argument("--format", choices=("json", "tsv"), default="json")
    args = parser.parse_args()

    client = GitHubClient()
    tags = (
        tag
        for tag in client.iter_tags(UPSTREAM_OWNER, UPSTREAM_NAME)
        if tag.name.endswith(UPSTREAM_TAG_SUFFIX)
    )
    released = client.published_release_tags(args.provider_owner, args.provider_repo)
    pending = discover_unreleased(tags, released, SemVer.parse(args.baseline))
    if args.format == "tsv":
        for item in pending:
            print(f"{item.name}\t{item.commit}")
    else:
        print(
            json.dumps(
                [{"tag": item.name, "commit": item.commit} for item in pending],
                ensure_ascii=False,
                separators=(",", ":"),
            )
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
