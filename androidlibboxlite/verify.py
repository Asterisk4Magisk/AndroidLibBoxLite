from __future__ import annotations

from dataclasses import dataclass
import hashlib
import io
import json
from pathlib import Path
import tempfile
from typing import Callable, Mapping
import zipfile

from .archive import validated_entries
from .errors import ReleaseError
from .lockfile import FORBIDDEN_TAGS, ReleaseLock


EXPECTED_ELF_MACHINE: Mapping[str, int] = {
    "arm64-v8a": 183,
    "armeabi-v7a": 40,
    "x86": 3,
    "x86_64": 62,
}
EXPECTED_GO_ARCH: Mapping[str, str] = {
    "arm64-v8a": "arm64",
    "armeabi-v7a": "arm",
    "x86": "386",
    "x86_64": "amd64",
}
REQUIRED_CLASSES = (
    "io/nekohasekai/libbox/Libbox.class",
    "io/nekohasekai/libbox/SetupOptions.class",
)
REQUIRED_SOURCES = (
    "io/nekohasekai/libbox/Libbox.java",
    "io/nekohasekai/libbox/SetupOptions.java",
)
FORBIDDEN_COMPILED_MODULES = frozenset(
    {
        "github.com/sagernet/sing-usbip",
        "github.com/sagernet/go-libtor",
    }
)


@dataclass(frozen=True)
class AbiReport:
    machine: int
    size: int
    sha256: str


@dataclass(frozen=True)
class VerificationReport:
    abis: Mapping[str, AbiReport]
    classes: tuple[str, ...]
    sources: tuple[str, ...]

    def to_canonical_json(self) -> bytes:
        value = {
            "abis": {
                abi: {"machine": item.machine, "size": item.size, "sha256": item.sha256}
                for abi, item in self.abis.items()
            },
            "classes": list(self.classes),
            "sources": list(self.sources),
        }
        return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode("utf-8")

    @classmethod
    def from_json(cls, encoded: bytes) -> "VerificationReport":
        try:
            value = json.loads(encoded.decode("utf-8"))
            if not isinstance(value, dict) or set(value) != {"abis", "classes", "sources"}:
                raise ValueError
            abis_raw = value["abis"]
            if not isinstance(abis_raw, dict):
                raise ValueError
            abis: dict[str, AbiReport] = {}
            for abi, item in abis_raw.items():
                if not isinstance(abi, str) or not isinstance(item, dict) or set(item) != {"machine", "size", "sha256"}:
                    raise ValueError
                machine, size, sha256 = item["machine"], item["size"], item["sha256"]
                if not isinstance(machine, int) or not isinstance(size, int) or not isinstance(sha256, str):
                    raise ValueError
                abis[abi] = AbiReport(machine=machine, size=size, sha256=sha256)
            classes = tuple(value["classes"])
            sources = tuple(value["sources"])
            if not all(isinstance(item, str) for item in classes + sources):
                raise ValueError
            return cls(abis=abis, classes=classes, sources=sources)
        except (UnicodeDecodeError, json.JSONDecodeError, TypeError, ValueError) as failure:
            raise ReleaseError("VERIFICATION_REPORT_INVALID", "verification report is invalid") from failure


BuildInfoReader = Callable[[Path, str], str]


def verify_release(
    lock: ReleaseLock,
    aar: Path,
    sources: Path,
    read_build_info: BuildInfoReader,
) -> VerificationReport:
    version = lock.source.tag.removeprefix("v").encode("ascii")
    reports: dict[str, AbiReport] = {}
    try:
        with zipfile.ZipFile(aar, "r") as archive:
            entries = validated_entries(archive)
            names = {item.filename for item in entries}
            if "classes.jar" not in names or "AndroidManifest.xml" not in names:
                _artifact_error("AAR is missing classes.jar or AndroidManifest.xml")
            classes = _nested_names(archive.read("classes.jar"), "classes.jar")
            if not set(REQUIRED_CLASSES).issubset(classes):
                _artifact_error("AAR is missing required libbox classes")
            _reject_forbidden_names(names)
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for abi in lock.libbox.abis:
                    entry = f"jni/{abi}/libbox.so"
                    if entry not in names:
                        _artifact_error(f"AAR is missing {entry}")
                    encoded = archive.read(entry)
                    machine = _elf_machine(encoded, abi)
                    if machine != EXPECTED_ELF_MACHINE[abi]:
                        _artifact_error(f"ELF machine differs for {abi}: {machine}")
                    if version not in encoded:
                        _artifact_error(f"sing-box version is absent from {abi} libbox.so")
                    binary = root / f"{abi}.so"
                    binary.write_bytes(encoded)
                    _verify_go_build_info(lock, abi, read_build_info(binary, abi))
                    reports[abi] = AbiReport(
                        machine=machine,
                        size=len(encoded),
                        sha256=hashlib.sha256(encoded).hexdigest(),
                    )
            actual_jni = {
                item.filename.split("/")[1]
                for item in entries
                if item.filename.startswith("jni/") and item.filename.endswith("/libbox.so")
            }
            if actual_jni != set(lock.libbox.abis):
                _artifact_error("AAR contains an unexpected JNI ABI")
        with zipfile.ZipFile(sources, "r") as source_archive:
            source_entries = validated_entries(source_archive)
            source_names = {item.filename for item in source_entries}
            if not set(REQUIRED_SOURCES).issubset(source_names):
                _artifact_error("source JAR is missing required libbox sources")
            _reject_forbidden_names(source_names)
    except ReleaseError:
        raise
    except (OSError, UnicodeError, zipfile.BadZipFile, RuntimeError) as failure:
        raise ReleaseError("ARTIFACT_INVALID", str(failure)) from failure
    return VerificationReport(
        abis={abi: reports[abi] for abi in lock.libbox.abis},
        classes=tuple(sorted(set(REQUIRED_CLASSES))),
        sources=tuple(sorted(set(REQUIRED_SOURCES))),
    )


def _nested_names(encoded: bytes, label: str) -> set[str]:
    try:
        with zipfile.ZipFile(io.BytesIO(encoded), "r") as archive:
            return {item.filename for item in validated_entries(archive)}
    except (zipfile.BadZipFile, RuntimeError) as failure:
        raise ReleaseError("ARTIFACT_INVALID", f"invalid nested {label}") from failure


def _elf_machine(encoded: bytes, abi: str) -> int:
    if len(encoded) < 20 or encoded[:4] != b"\x7fELF" or encoded[5] not in (1, 2):
        _artifact_error(f"invalid ELF header for {abi}")
    byteorder = "little" if encoded[5] == 1 else "big"
    return int.from_bytes(encoded[18:20], byteorder)


def _verify_go_build_info(lock: ReleaseLock, abi: str, output: str) -> None:
    lines = output.splitlines()
    if not lines or not lines[0].endswith(f": {lock.toolchain.go.version}"):
        _artifact_error(f"Go version differs for {abi}")
    settings: dict[str, str] = {}
    dependencies: set[str] = set()
    for line in lines[1:]:
        fields = line.split("\t")
        if len(fields) >= 3 and fields[1] == "build" and "=" in fields[2]:
            key, value = fields[2].split("=", 1)
            settings[key] = value
        if len(fields) >= 3 and fields[1] == "dep":
            dependencies.add(fields[2])
    expected = {
        "-tags": ",".join(lock.libbox.tags),
        "-trimpath": "true",
        "CGO_ENABLED": "1",
        "GOARCH": EXPECTED_GO_ARCH[abi],
        "GOOS": "android",
    }
    if any(settings.get(key) != value for key, value in expected.items()):
        _artifact_error(f"Go build settings differ for {abi}")
    if FORBIDDEN_TAGS.intersection(settings.get("-tags", "").split(",")):
        _policy_error(f"forbidden build tag compiled for {abi}")
    forbidden = dependencies.intersection(FORBIDDEN_COMPILED_MODULES)
    if forbidden:
        _policy_error(f"forbidden compiled module for {abi}: {sorted(forbidden)}")


def _reject_forbidden_names(names: set[str]) -> None:
    for name in names:
        parts = [part.casefold() for part in name.split("/")]
        if any(part == "tor" or part.startswith("libtor") or part.startswith("tor-") for part in parts):
            _policy_error(f"forbidden embedded Tor entry: {name}")


def _artifact_error(message: str) -> None:
    raise ReleaseError("ARTIFACT_INVALID", message)


def _policy_error(message: str) -> None:
    raise ReleaseError("ARTIFACT_POLICY_INVALID", message)
