#!/usr/bin/env python3
from __future__ import annotations

import argparse
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.archive import normalize_aar
from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock


def main() -> int:
    parser = argparse.ArgumentParser(description="规范化 AndroidLibBoxLite AAR")
    parser.add_argument("--lock", type=Path, required=True)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        lock = ReleaseLock.from_json(args.lock.read_bytes())
        normalize_aar(args.input, args.output, lock.libbox.abis)
        print(args.output)
        return 0
    except (OSError, ReleaseError) as failure:
        print(str(failure), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
