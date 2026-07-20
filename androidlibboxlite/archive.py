from __future__ import annotations

import os
import posixpath
from pathlib import Path, PurePosixPath
import shutil
import stat
import tarfile
import zipfile

from .errors import ReleaseError


_MAX_ENTRIES = 200_000
_MAX_ENTRY_BYTES = 1024 * 1024 * 1024
_MAX_TOTAL_BYTES = 2 * 1024 * 1024 * 1024
_MAX_TOOLCHAIN_TOTAL_BYTES = 8 * 1024 * 1024 * 1024
_FIXED_TIMESTAMP = (1980, 1, 1, 0, 0, 0)


def safe_extract_zip(source: Path, destination: Path) -> None:
    source = source.resolve()
    destination = destination.resolve()
    if destination.exists():
        raise ReleaseError("ARCHIVE_INVALID", f"extraction destination already exists: {destination}")
    try:
        with zipfile.ZipFile(source, "r") as archive:
            entries = validated_entries(
                archive,
                allow_links=True,
                allow_case_collisions=True,
                max_total_bytes=_MAX_TOOLCHAIN_TOTAL_BYTES,
            )
            by_name = {info.filename.rstrip("/"): info for info in entries}
            resolved_links: dict[str, zipfile.ZipInfo] = {}
            total = sum(info.file_size for info in entries)
            for info in entries:
                if _is_zip_link(info):
                    target_info = _resolve_zip_link(info, by_name, archive)
                    total += target_info.file_size
                    if total > _MAX_TOOLCHAIN_TOTAL_BYTES:
                        raise ReleaseError(
                            "ARCHIVE_INVALID",
                            "materialized ZIP links exceed the total size limit",
                        )
                    resolved_links[info.filename] = target_info
            destination.mkdir(parents=True)
            for info in entries:
                target = destination.joinpath(*PurePosixPath(info.filename).parts)
                if info.is_dir():
                    target.mkdir(parents=True, exist_ok=True)
                    target.chmod(0o755)
                    continue
                target.parent.mkdir(parents=True, exist_ok=True)
                content_info = resolved_links.get(info.filename, info)
                with archive.open(content_info, "r") as input_file, target.open("xb") as output_file:
                    shutil.copyfileobj(input_file, output_file, length=1024 * 1024)
                target.chmod(_sanitized_mode(content_info.external_attr >> 16))
    except ReleaseError:
        if destination.exists():
            shutil.rmtree(destination)
        raise
    except (OSError, zipfile.BadZipFile, RuntimeError) as failure:
        if destination.exists():
            shutil.rmtree(destination)
        raise ReleaseError("ARCHIVE_INVALID", f"cannot extract {source}: {failure}") from failure


def safe_extract_tar_gz(source: Path, destination: Path) -> None:
    source = source.resolve()
    destination = destination.resolve()
    if destination.exists():
        raise ReleaseError("ARCHIVE_INVALID", f"extraction destination already exists: {destination}")
    try:
        with tarfile.open(source, "r:gz") as archive:
            members = archive.getmembers()
            if not members or len(members) > _MAX_ENTRIES:
                raise ReleaseError("ARCHIVE_INVALID", "tar entry count is outside the reviewed range")
            seen: set[str] = set()
            folded: set[str] = set()
            total = 0
            by_name: dict[str, tarfile.TarInfo] = {}
            for member in members:
                path = PurePosixPath(member.name)
                if (
                    not member.name
                    or "\\" in member.name
                    or member.name.startswith("/")
                    or any(part in ("", ".", "..") for part in path.parts)
                    or any(":" in part or "\x00" in part for part in path.parts)
                    or not (member.isdir() or member.isfile() or member.issym() or member.islnk())
                ):
                    raise ReleaseError("ARCHIVE_INVALID", f"unsafe tar entry: {member.name!r}")
                folded_name = member.name.casefold()
                if member.name in seen or folded_name in folded:
                    raise ReleaseError("ARCHIVE_INVALID", f"duplicate tar entry: {member.name!r}")
                seen.add(member.name)
                folded.add(folded_name)
                by_name[member.name] = member
                if member.size < 0 or member.size > _MAX_ENTRY_BYTES:
                    raise ReleaseError("ARCHIVE_INVALID", f"tar entry exceeds size limit: {member.name!r}")
                total += member.size
                if total > _MAX_TOTAL_BYTES:
                    raise ReleaseError("ARCHIVE_INVALID", "tar expands beyond the total size limit")
            resolved_links: dict[str, tarfile.TarInfo] = {}
            for member in members:
                if member.issym() or member.islnk():
                    target = _resolve_tar_link(member, by_name)
                    total += target.size
                    if total > _MAX_TOTAL_BYTES:
                        raise ReleaseError("ARCHIVE_INVALID", "materialized tar links exceed the total size limit")
                    resolved_links[member.name] = target
            destination.mkdir(parents=True)
            for member in members:
                target = destination.joinpath(*PurePosixPath(member.name).parts)
                if member.isdir():
                    target.mkdir(parents=True, exist_ok=True)
                    target.chmod(0o755)
                    continue
                target.parent.mkdir(parents=True, exist_ok=True)
                content_member = resolved_links.get(member.name, member)
                source_file = archive.extractfile(content_member)
                if source_file is None:
                    raise ReleaseError("ARCHIVE_INVALID", f"tar file content is missing: {member.name!r}")
                with source_file, target.open("xb") as output_file:
                    shutil.copyfileobj(source_file, output_file, length=1024 * 1024)
                target.chmod(_sanitized_mode(content_member.mode))
    except ReleaseError:
        if destination.exists():
            shutil.rmtree(destination)
        raise
    except (OSError, tarfile.TarError) as failure:
        if destination.exists():
            shutil.rmtree(destination)
        raise ReleaseError("ARCHIVE_INVALID", f"cannot extract {source}: {failure}") from failure


def normalize_aar(source: Path, destination: Path, required_abis: tuple[str, ...]) -> None:
    source = source.resolve()
    destination = destination.resolve()
    if source == destination or not source.is_file():
        raise ReleaseError("ARCHIVE_INVALID", "normalization source and destination are invalid")
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.{os.getpid()}.tmp")
    try:
        with zipfile.ZipFile(source, "r") as input_archive:
            entries = validated_entries(input_archive)
            _validate_aar_abis(entries, required_abis)
            with zipfile.ZipFile(
                temporary,
                "x",
                compression=zipfile.ZIP_DEFLATED,
                compresslevel=9,
                strict_timestamps=True,
            ) as output_archive:
                for original in sorted(entries, key=lambda item: item.filename.encode("utf-8")):
                    normalized = zipfile.ZipInfo(original.filename, _FIXED_TIMESTAMP)
                    normalized.create_system = 3
                    normalized.flag_bits = 0x800
                    normalized.compress_type = zipfile.ZIP_DEFLATED
                    normalized.external_attr = (
                        ((stat.S_IFDIR | 0o755) if original.is_dir() else (stat.S_IFREG | 0o644)) << 16
                    )
                    content = b"" if original.is_dir() else input_archive.read(original)
                    output_archive.writestr(
                        normalized,
                        content,
                        compress_type=zipfile.ZIP_DEFLATED,
                        compresslevel=9,
                    )
        os.replace(temporary, destination)
    except ReleaseError:
        raise
    except (OSError, zipfile.BadZipFile, RuntimeError) as failure:
        raise ReleaseError("ARCHIVE_INVALID", f"cannot normalize {source}: {failure}") from failure
    finally:
        if temporary.exists():
            temporary.unlink()


def validated_entries(
    archive: zipfile.ZipFile,
    *,
    allow_links: bool = False,
    allow_case_collisions: bool = False,
    max_total_bytes: int = _MAX_TOTAL_BYTES,
) -> list[zipfile.ZipInfo]:
    entries = archive.infolist()
    if not entries or len(entries) > _MAX_ENTRIES:
        raise ReleaseError("ARCHIVE_INVALID", "archive entry count is outside the reviewed range")
    exact: set[str] = set()
    folded: set[str] = set()
    total = 0
    for info in entries:
        name = info.filename
        path = PurePosixPath(name)
        if (
            not name
            or "\\" in name
            or name.startswith("/")
            or any(part in ("", ".", "..") for part in path.parts)
            or any(":" in part or "\x00" in part for part in path.parts)
        ):
            raise ReleaseError("ARCHIVE_INVALID", f"unsafe archive path: {name!r}")
        folded_name = name.casefold()
        if name in exact or (not allow_case_collisions and folded_name in folded):
            raise ReleaseError("ARCHIVE_INVALID", f"duplicate or case-colliding path: {name!r}")
        exact.add(name)
        folded.add(folded_name)
        if info.flag_bits & 0x1:
            raise ReleaseError("ARCHIVE_INVALID", f"encrypted archive entry: {name!r}")
        if _is_zip_link(info) and not allow_links:
            raise ReleaseError("ARCHIVE_INVALID", f"symbolic-link archive entry: {name!r}")
        if info.file_size < 0 or info.file_size > _MAX_ENTRY_BYTES:
            raise ReleaseError("ARCHIVE_INVALID", f"entry exceeds size limit: {name!r}")
        total += info.file_size
        if total > max_total_bytes:
            raise ReleaseError("ARCHIVE_INVALID", "archive expands beyond the total size limit")
    return entries


def _validate_aar_abis(entries: list[zipfile.ZipInfo], required_abis: tuple[str, ...]) -> None:
    actual: set[str] = set()
    for info in entries:
        parts = PurePosixPath(info.filename).parts
        if not parts or parts[0] != "jni":
            continue
        if len(parts) != 3 or parts[2] != "libbox.so" or info.is_dir():
            raise ReleaseError("ARCHIVE_INVALID", f"unexpected JNI entry: {info.filename!r}")
        actual.add(parts[1])
    if actual != set(required_abis):
        raise ReleaseError(
            "ARCHIVE_INVALID",
            f"JNI ABI set differs: expected {list(required_abis)}, got {sorted(actual)}",
        )


def _sanitized_mode(mode: int) -> int:
    return 0o755 if mode & 0o111 else 0o644


def _is_zip_link(info: zipfile.ZipInfo) -> bool:
    return stat.S_ISLNK(info.external_attr >> 16)


def _resolve_zip_link(
    info: zipfile.ZipInfo,
    by_name: dict[str, zipfile.ZipInfo],
    archive: zipfile.ZipFile,
) -> zipfile.ZipInfo:
    current = info
    visited: set[str] = set()
    for _ in range(32):
        current_name = current.filename.rstrip("/")
        if current_name in visited:
            raise ReleaseError("ARCHIVE_INVALID", f"ZIP link cycle: {info.filename!r}")
        visited.add(current_name)
        try:
            link = archive.read(current).decode("utf-8")
        except UnicodeDecodeError as failure:
            raise ReleaseError(
                "ARCHIVE_INVALID",
                f"ZIP link target is not UTF-8: {info.filename!r}",
            ) from failure
        if not link or "\\" in link or "\x00" in link or link.startswith("/"):
            raise ReleaseError("ARCHIVE_INVALID", f"unsafe ZIP link target: {info.filename!r}")
        normalized = posixpath.normpath(
            (PurePosixPath(current_name).parent / link).as_posix()
        )
        target_path = PurePosixPath(normalized)
        if normalized in ("", ".", "..") or normalized.startswith("../") or any(
            part in ("", ".", "..") or ":" in part for part in target_path.parts
        ):
            raise ReleaseError("ARCHIVE_INVALID", f"ZIP link escapes the archive: {info.filename!r}")
        target = by_name.get(normalized)
        if target is None:
            raise ReleaseError("ARCHIVE_INVALID", f"ZIP link target is missing: {info.filename!r}")
        if target.is_dir():
            raise ReleaseError("ARCHIVE_INVALID", f"ZIP link target is not a file: {info.filename!r}")
        if not _is_zip_link(target):
            return target
        current = target
    raise ReleaseError("ARCHIVE_INVALID", f"ZIP link chain is too deep: {info.filename!r}")


def _resolve_tar_link(
    member: tarfile.TarInfo,
    by_name: dict[str, tarfile.TarInfo],
) -> tarfile.TarInfo:
    current = member
    visited: set[str] = set()
    for _ in range(32):
        if current.name in visited:
            raise ReleaseError("ARCHIVE_INVALID", f"tar link cycle: {member.name!r}")
        visited.add(current.name)
        link = current.linkname
        if not link or "\\" in link or "\x00" in link or link.startswith("/"):
            raise ReleaseError("ARCHIVE_INVALID", f"unsafe tar link target: {member.name!r}")
        base = PurePosixPath(current.name).parent if current.issym() else PurePosixPath()
        normalized = posixpath.normpath((base / link).as_posix())
        target_path = PurePosixPath(normalized)
        if normalized in ("", ".", "..") or normalized.startswith("../") or any(
            part in ("", ".", "..") or ":" in part for part in target_path.parts
        ):
            raise ReleaseError("ARCHIVE_INVALID", f"tar link escapes the archive: {member.name!r}")
        target = by_name.get(normalized)
        if target is None:
            raise ReleaseError("ARCHIVE_INVALID", f"tar link target is missing: {member.name!r}")
        if target.isfile():
            return target
        if not (target.issym() or target.islnk()):
            raise ReleaseError("ARCHIVE_INVALID", f"tar link target is not a file: {member.name!r}")
        current = target
    raise ReleaseError("ARCHIVE_INVALID", f"tar link chain is too deep: {member.name!r}")
