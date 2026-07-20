#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.build import build_libbox
from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock


def main() -> int:
    parser = argparse.ArgumentParser(description="使用冻结工具链构建 Android libbox AAR")
    parser.add_argument("--lock", type=Path, required=True)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=Path(".toolchains/downloads"))
    args = parser.parse_args()
    try:
        lock = ReleaseLock.from_json(args.lock.read_bytes())
        outputs = build_libbox(lock, args.workspace, args.output, args.cache)
        print(
            json.dumps(
                {"aar": str(outputs.raw_aar), "sources": str(outputs.sources), "go": str(outputs.go)},
                separators=(",", ":"),
            )
        )
        return 0
    except (OSError, ReleaseError) as failure:
        print(str(failure), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
