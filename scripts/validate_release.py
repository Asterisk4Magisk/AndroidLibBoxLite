#!/usr/bin/env python3
from __future__ import annotations

import argparse
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.github_api import GitHubClient
from androidlibboxlite.release import validate_release_identity


def main() -> int:
    parser = argparse.ArgumentParser(description="验证待发布 tag、锁文件与上游身份")
    parser.add_argument("--tag", required=True)
    parser.add_argument("--locks", type=Path, default=Path("locks"))
    parser.add_argument("--github-output", type=Path, required=True)
    args = parser.parse_args()
    try:
        client = GitHubClient()
        matches = [item for item in client.iter_tags("SagerNet", "sing-box") if item.name == args.tag]
        if len(matches) != 1:
            raise ReleaseError("UPSTREAM_TAG_MOVED", f"upstream tag is missing or ambiguous: {args.tag}")
        identity = validate_release_identity(args.tag, args.locks, matches[0].commit)
        timestamp = client.commit_timestamp("SagerNet", "sing-box", matches[0].commit)
        if timestamp != identity.lock.source.commit_time:
            raise ReleaseError("UPSTREAM_TAG_MOVED", f"upstream timestamp differs for {args.tag}")
        with args.github_output.open("a", encoding="utf-8", newline="\n") as output:
            output.write(f"tag={identity.lock.source.tag}\n")
            output.write(f"lock={identity.path.as_posix()}\n")
            output.write(f"prerelease={'true' if identity.prerelease else 'false'}\n")
        return 0
    except (OSError, ReleaseError) as failure:
        print(str(failure), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
