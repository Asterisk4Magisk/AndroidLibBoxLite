from __future__ import annotations

from dataclasses import dataclass
from functools import total_ordering
import re
from typing import Iterable

from .errors import ReleaseError


_TAG_PATTERN = re.compile(
    r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$"
)
_COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")


@total_ordering
@dataclass(frozen=True)
class SemVer:
    major: int
    minor: int
    patch: int
    prerelease: tuple[int | str, ...]
    tag: str

    @classmethod
    def parse(cls, tag: str) -> "SemVer":
        match = _TAG_PATTERN.fullmatch(tag)
        if match is None:
            raise ReleaseError("UPSTREAM_TAG_INVALID", f"noncanonical tag: {tag!r}")
        prerelease: list[int | str] = []
        if match.group(4):
            for identifier in match.group(4).split("."):
                if identifier.isdigit():
                    if len(identifier) > 1 and identifier.startswith("0"):
                        raise ReleaseError(
                            "UPSTREAM_TAG_INVALID",
                            f"numeric prerelease identifier has a leading zero: {tag!r}",
                        )
                    prerelease.append(int(identifier))
                else:
                    prerelease.append(identifier)
        return cls(
            major=int(match.group(1)),
            minor=int(match.group(2)),
            patch=int(match.group(3)),
            prerelease=tuple(prerelease),
            tag=tag,
        )

    def __lt__(self, other: object) -> bool:
        if not isinstance(other, SemVer):
            return NotImplemented
        core = (self.major, self.minor, self.patch)
        other_core = (other.major, other.minor, other.patch)
        if core != other_core:
            return core < other_core
        if not self.prerelease:
            return False if not other.prerelease else False
        if not other.prerelease:
            return True
        for left, right in zip(self.prerelease, other.prerelease, strict=False):
            if left == right:
                continue
            if isinstance(left, int) and isinstance(right, str):
                return True
            if isinstance(left, str) and isinstance(right, int):
                return False
            return left < right
        return len(self.prerelease) < len(other.prerelease)

    @property
    def is_prerelease(self) -> bool:
        return bool(self.prerelease)


@dataclass(frozen=True)
class GitTag:
    name: str
    commit: str

    def __post_init__(self) -> None:
        if _COMMIT_PATTERN.fullmatch(self.commit) is None:
            raise ReleaseError("UPSTREAM_COMMIT_INVALID", f"invalid commit for {self.name!r}")


def discover_unreleased(
    tags: Iterable[GitTag],
    released: set[str],
    baseline: SemVer,
) -> list[GitTag]:
    canonical: dict[str, tuple[SemVer, GitTag]] = {}
    for tag in tags:
        try:
            version = SemVer.parse(tag.name)
        except ReleaseError:
            continue
        previous = canonical.get(tag.name)
        if previous is not None and previous[1].commit != tag.commit:
            raise ReleaseError("UPSTREAM_TAG_MOVED", f"conflicting objects for {tag.name}")
        canonical[tag.name] = (version, tag)
    return [
        tag
        for version, tag in sorted(canonical.values(), key=lambda item: item[0])
        if version >= baseline and tag.name not in released
    ]
