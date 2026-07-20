#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import ReleaseLock
from androidlibboxlite.process import run_checked
from androidlibboxlite.verify import verify_release


def main() -> int:
    parser = argparse.ArgumentParser(description="验证 AndroidLibBoxLite release 资产")
    parser.add_argument("--lock", type=Path, required=True)
    parser.add_argument("--aar", type=Path, required=True)
    parser.add_argument("--sources", type=Path, required=True)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    try:
        lock = ReleaseLock.from_json(args.lock.read_bytes())
        args.report.parent.mkdir(parents=True, exist_ok=True)

        def read_build_info(path: Path, abi: str) -> str:
            del abi
            environment = {
                "HOME": str(args.report.parent.resolve()),
                "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8",
                "PATH": os.pathsep.join((str(args.go.parent.resolve()), "/usr/bin", "/bin")),
                "TZ": "UTC",
            }
            return run_checked(
                [str(args.go.resolve()), "version", "-m", str(path)],
                args.report.parent.resolve(),
                environment,
                60,
                "GO_BUILD_INFO_FAILED",
            ).stdout

        report = verify_release(lock, args.aar, args.sources, read_build_info)
        args.report.write_bytes(report.to_canonical_json())
        print(args.report)
        return 0
    except (OSError, ReleaseError) as failure:
        print(str(failure), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
