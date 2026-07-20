from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from .errors import ReleaseError
from .lockfile import ReleaseLock
from .semver import SemVer


_BASELINE = SemVer.parse("v1.14.0-alpha.47")


@dataclass(frozen=True)
class ReleaseIdentity:
    path: Path
    lock: ReleaseLock
    prerelease: bool


def validate_release_identity(tag: str, locks: Path, upstream_commit: str) -> ReleaseIdentity:
    version = SemVer.parse(tag)
    if version < _BASELINE:
        raise ReleaseError("RELEASE_IDENTITY_INVALID", f"tag predates the baseline: {tag}")
    root = locks.resolve()
    path = (root / f"{version.tag}.json").resolve()
    if path.parent != root or path.name != f"{version.tag}.json" or not path.is_file():
        raise ReleaseError("RELEASE_IDENTITY_INVALID", f"lock does not exist for {tag}")
    encoded = path.read_bytes()
    lock = ReleaseLock.from_json(encoded)
    if encoded != lock.to_canonical_json():
        raise ReleaseError("RELEASE_IDENTITY_INVALID", f"lock is not canonical: {path}")
    if lock.source.tag != version.tag or lock.source.commit != upstream_commit:
        raise ReleaseError("UPSTREAM_TAG_MOVED", f"upstream identity differs for {tag}")
    return ReleaseIdentity(path=path, lock=lock, prerelease=version.is_prerelease)
