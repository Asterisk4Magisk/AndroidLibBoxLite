from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path

from .errors import ReleaseError
from .lockfile import ReleaseLock
from .verify import VerificationReport


def write_release_metadata(
    lock: ReleaseLock,
    report: VerificationReport,
    aar: Path,
    sources: Path,
    output: Path,
) -> tuple[Path, Path]:
    if aar.name != "libbox.aar" or sources.name != "libbox-sources.jar":
        raise ReleaseError("MANIFEST_INPUT_INVALID", "release asset names are not canonical")
    output.mkdir(parents=True, exist_ok=True)
    artifacts = {
        aar.name: _file_identity(aar),
        sources.name: _file_identity(sources),
    }
    manifest_value = {
        "schema": 1,
        "release": {
            "tag": lock.source.tag,
            "prerelease": bool(lock.source.tag.split("-", 1)[1:]),
            "workflowCommit": lock.workflow_commit,
            "lockSha256": hashlib.sha256(lock.to_canonical_json()).hexdigest(),
        },
        "source": {
            "repository": lock.source.repository,
            "tag": lock.source.tag,
            "commit": lock.source.commit,
            "commitTime": lock.source.commit_time,
            "archiveSha256": lock.source.archive.sha256,
        },
        "toolchain": lock.to_dict()["toolchain"],
        "libbox": lock.to_dict()["libbox"],
        "verification": {
            "abis": {
                abi: {
                    "machine": item.machine,
                    "size": item.size,
                    "sha256": item.sha256,
                }
                for abi, item in report.abis.items()
            },
            "classes": list(report.classes),
            "sources": list(report.sources),
        },
        "artifacts": artifacts,
    }
    manifest = output / "build-manifest.json"
    sums = output / "SHA256SUMS"
    _atomic_write(
        manifest,
        (json.dumps(manifest_value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8"),
    )
    checksum_files = (manifest, sources, aar)
    checksum_lines = [
        f"{_sha256(path)}  {path.name}\n" for path in sorted(checksum_files, key=lambda item: item.name)
    ]
    _atomic_write(sums, "".join(checksum_lines).encode("ascii"))
    return manifest, sums


def _file_identity(path: Path) -> dict[str, object]:
    if not path.is_file():
        raise ReleaseError("MANIFEST_INPUT_INVALID", f"asset is missing: {path}")
    return {"size": path.stat().st_size, "sha256": _sha256(path)}


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def _atomic_write(path: Path, content: bytes) -> None:
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("xb") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()
