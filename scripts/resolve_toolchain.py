#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.github_api import GitHubClient
from androidlibboxlite.lockfile import ReleaseLock
from androidlibboxlite.toolchains import ArchiveCache, resolve_release_lock


def _git_head() -> str:
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        timeout=10,
    )
    if result.returncode != 0:
        raise ReleaseError("WORKFLOW_COMMIT_INVALID", result.stderr.strip() or "git rev-parse failed")
    return result.stdout.strip().lower()


def _verify_upstream(tag: str, commit: str) -> int:
    client = GitHubClient()
    matches = [item for item in client.iter_tags("SagerNet", "sing-box") if item.name == tag]
    if len(matches) != 1 or matches[0].commit != commit:
        raise ReleaseError("UPSTREAM_TAG_MOVED", f"{tag} does not resolve to {commit}")
    return client.commit_timestamp("SagerNet", "sing-box", commit)


def _write_immutable(path: Path, lock: ReleaseLock) -> None:
    encoded = lock.to_canonical_json()
    if path.exists():
        existing = ReleaseLock.from_json(path.read_bytes()).to_canonical_json()
        if existing != encoded:
            raise ReleaseError("LOCK_IMMUTABLE", f"existing lock differs: {path}")
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("xb") as output:
            output.write(encoded)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def main() -> int:
    parser = argparse.ArgumentParser(description="解析并冻结 AndroidLibBoxLite 每版本工具链")
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=Path(".toolchains/downloads"))
    parser.add_argument("--workflow-commit")
    args = parser.parse_args()
    try:
        if args.output.exists():
            existing = ReleaseLock.from_json(args.output.read_bytes())
            if existing.source.tag != args.tag or existing.source.commit != args.commit.lower():
                raise ReleaseError("LOCK_IMMUTABLE", f"existing lock has another source: {args.output}")
            print(args.output)
            return 0
        commit_time = _verify_upstream(args.tag, args.commit.lower())
        lock = resolve_release_lock(
            tag=args.tag,
            commit=args.commit.lower(),
            commit_time=commit_time,
            workflow_commit=(args.workflow_commit or _git_head()).lower(),
            cache=ArchiveCache(args.cache),
        )
        _write_immutable(args.output, lock)
        print(args.output)
        return 0
    except (OSError, ReleaseError) as failure:
        print(str(failure), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
