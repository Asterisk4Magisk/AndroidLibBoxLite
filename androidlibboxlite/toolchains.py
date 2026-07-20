from __future__ import annotations

from dataclasses import dataclass
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import time
from typing import Callable
import urllib.error
import urllib.request
from urllib.parse import urlparse
import xml.etree.ElementTree as ElementTree

from .errors import ReleaseError
from .lockfile import (
    ALLOWED_DOWNLOAD_HOSTS,
    LOCK_SCHEMA,
    REQUIRED_ABIS,
    REQUIRED_TAGS,
    AndroidPackagePin,
    AndroidPin,
    ArchivePin,
    GoModulePin,
    GoPin,
    JdkPin,
    LibboxPin,
    ReleaseLock,
    SourcePin,
    ToolchainPin,
    libbox_ldflags,
)


ANDROID_REPOSITORY_URL = "https://dl.google.com/android/repository/repository2-3.xml"


@dataclass(frozen=True)
class GoSelection:
    version: str
    archive: ArchivePin


@dataclass(frozen=True)
class JdkSelection:
    vendor: str
    version: str
    archive: ArchivePin


@dataclass(frozen=True)
class GoModuleSelection:
    module: str
    version: str
    sum: str


@dataclass(frozen=True)
class AndroidPackageSelection:
    package: str
    url: str
    size: int
    sha1: str


@dataclass(frozen=True)
class AndroidSelection:
    command_line_tools: AndroidPackageSelection
    platform: AndroidPackageSelection
    build_tools: AndroidPackageSelection
    ndk: AndroidPackageSelection


ArchivePinner = Callable[[str, int | None, str | None], ArchivePin]


class MetadataClient:
    def __init__(self, retries: int = 3) -> None:
        self._retries = retries

    def json(self, url: str, limit: int = 8 * 1024 * 1024) -> object:
        try:
            return json.loads(self.bytes(url, limit).decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as failure:
            raise ReleaseError("TOOLCHAIN_METADATA_INVALID", f"metadata is not UTF-8 JSON: {url}") from failure

    def text(self, url: str, limit: int = 1024 * 1024) -> str:
        try:
            return self.bytes(url, limit).decode("utf-8")
        except UnicodeDecodeError as failure:
            raise ReleaseError("TOOLCHAIN_METADATA_INVALID", f"metadata is not UTF-8 text: {url}") from failure

    def bytes(self, url: str, limit: int) -> bytes:
        _require_download_url(url)
        headers = {"user-agent": "AndroidLibBoxLite/0.1"}
        for attempt in range(1, self._retries + 1):
            request = urllib.request.Request(url, headers=headers, method="GET")
            try:
                with urllib.request.urlopen(request, timeout=60) as response:
                    body = response.read(limit + 1)
                    if len(body) > limit:
                        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", f"metadata exceeds size limit: {url}")
                    return body
            except urllib.error.HTTPError as failure:
                if failure.code < 500 or attempt == self._retries:
                    raise ReleaseError("TOOLCHAIN_REQUEST_FAILED", f"HTTP {failure.code}: {url}") from failure
            except urllib.error.URLError as failure:
                if attempt == self._retries:
                    raise ReleaseError("TOOLCHAIN_REQUEST_FAILED", f"request failed: {url}: {failure}") from failure
            time.sleep(attempt)
        raise AssertionError("unreachable")


class ArchiveCache:
    def __init__(self, root: Path) -> None:
        self.root = root.resolve()
        self.root.mkdir(parents=True, exist_ok=True)

    def pin(self, url: str, expected_size: int | None, expected_sha1: str | None) -> ArchivePin:
        _require_download_url(url)
        _, identity = self._obtain(url, expected_size, expected_sha1, None)
        return identity

    def materialize(self, pin: ArchivePin) -> Path:
        destination, _ = self._obtain(pin.url, pin.size, pin.sha1, pin.sha256)
        return destination

    def _obtain(
        self,
        url: str,
        expected_size: int | None,
        expected_sha1: str | None,
        expected_sha256: str | None,
    ) -> tuple[Path, ArchivePin]:
        destination = self._path(url)
        if destination.is_file():
            try:
                identity = _hash_archive(destination, url, expected_size, expected_sha1)
                _require_sha256(identity, expected_sha256)
                return destination, identity
            except ReleaseError:
                destination.unlink()
        failure: ReleaseError | None = None
        for attempt in range(1, 4):
            partial = self.root / f".{destination.stem}.{os.getpid()}.{attempt}.partial"
            try:
                self._download(url, partial, expected_size)
                identity = _hash_archive(partial, url, expected_size, expected_sha1)
                _require_sha256(identity, expected_sha256)
                os.replace(partial, destination)
                return destination, identity
            except ReleaseError as caught:
                failure = caught
                if attempt < 3:
                    time.sleep(attempt)
            finally:
                if partial.exists():
                    partial.unlink()
        assert failure is not None
        raise failure

    def _path(self, url: str) -> Path:
        identity = hashlib.sha256(url.encode("utf-8")).hexdigest()
        suffix = Path(urlparse(url).path).suffix or ".archive"
        return self.root / f"{identity}{suffix}"

    @staticmethod
    def _download(url: str, destination: Path, expected_size: int | None) -> None:
        request = urllib.request.Request(url, headers={"user-agent": "AndroidLibBoxLite/0.1"}, method="GET")
        try:
            with urllib.request.urlopen(request, timeout=300) as response, destination.open("xb") as output:
                total = 0
                declared_header = response.headers.get("Content-Length")
                declared_size = int(declared_header) if declared_header and declared_header.isdigit() else None
                while True:
                    block = response.read(1024 * 1024)
                    if not block:
                        break
                    total += len(block)
                    if expected_size is not None and total > expected_size:
                        raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"download exceeds expected size: {url}")
                    output.write(block)
                required_size = expected_size if expected_size is not None else declared_size
                if required_size is not None and total != required_size:
                    raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"download size mismatch: {url}")
        except (OSError, http.client.HTTPException, urllib.error.URLError) as failure:
            if isinstance(failure, ReleaseError):
                raise
            raise ReleaseError("TOOLCHAIN_REQUEST_FAILED", f"archive download failed: {url}: {failure}") from failure


def resolve_release_lock(
    tag: str,
    commit: str,
    commit_time: int,
    workflow_commit: str,
    cache: ArchiveCache,
    client: MetadataClient | None = None,
) -> ReleaseLock:
    metadata = client or MetadataClient()
    go = select_go(metadata.json("https://go.dev/dl/?mode=json"))
    adoptium_info = metadata.json("https://api.adoptium.net/v3/info/available_releases")
    if not isinstance(adoptium_info, dict) or not isinstance(adoptium_info.get("most_recent_lts"), int):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Adoptium LTS metadata is invalid")
    feature = adoptium_info["most_recent_lts"]
    adoptium_url = (
        f"https://api.adoptium.net/v3/assets/latest/{feature}/hotspot"
        "?architecture=x64&heap_size=normal&image_type=jdk&jvm_impl=hotspot"
        "&os=linux&project=jdk&vendor=eclipse"
    )
    jdk = select_adoptium(adoptium_info, metadata.json(adoptium_url))
    gomobile_latest = metadata.json("https://proxy.golang.org/github.com/sagernet/gomobile/@latest")
    if not isinstance(gomobile_latest, dict) or not isinstance(gomobile_latest.get("Version"), str):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "gomobile version metadata is invalid")
    gomobile_version = gomobile_latest["Version"]
    gomobile = select_gomobile(
        gomobile_latest,
        metadata.text(
            f"https://sum.golang.org/lookup/github.com/sagernet/gomobile@{gomobile_version}"
        ),
    )
    android_xml = metadata.bytes(ANDROID_REPOSITORY_URL, 8 * 1024 * 1024)
    android = parse_android_repository(android_xml)
    source_archive = cache.pin(
        f"https://codeload.github.com/SagerNet/sing-box/zip/{commit}",
        None,
        None,
    )

    def pin_android(item: AndroidPackageSelection) -> AndroidPackagePin:
        return AndroidPackagePin(
            package=item.package,
            archive=cache.pin(item.url, item.size, item.sha1),
        )

    lock = ReleaseLock(
        schema=LOCK_SCHEMA,
        source=SourcePin(
            repository="SagerNet/sing-box",
            tag=tag,
            commit=commit,
            commit_time=commit_time,
            archive=source_archive,
        ),
        toolchain=ToolchainPin(
            go=GoPin(version=go.version, archive=go.archive),
            gomobile=GoModulePin(module=gomobile.module, version=gomobile.version, sum=gomobile.sum),
            jdk=JdkPin(vendor=jdk.vendor, version=jdk.version, archive=jdk.archive),
            android=AndroidPin(
                repository=ANDROID_REPOSITORY_URL,
                command_line_tools=pin_android(android.command_line_tools),
                platform=pin_android(android.platform),
                build_tools=pin_android(android.build_tools),
                ndk=pin_android(android.ndk),
            ),
        ),
        libbox=LibboxPin(
            android_api=23,
            abis=REQUIRED_ABIS,
            tags=REQUIRED_TAGS,
            ldflags=libbox_ldflags(tag),
        ),
        workflow_commit=workflow_commit,
    )
    return ReleaseLock.from_json(lock.to_canonical_json())


def select_go(payload: object) -> GoSelection:
    if not isinstance(payload, list):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Go release payload is not an array")
    for release in payload:
        if not isinstance(release, dict) or release.get("stable") is not True:
            continue
        version = release.get("version")
        files = release.get("files")
        if not isinstance(version, str) or not isinstance(files, list):
            continue
        for item in files:
            if not isinstance(item, dict):
                continue
            if (item.get("os"), item.get("arch"), item.get("kind")) != ("linux", "amd64", "archive"):
                continue
            filename = item.get("filename")
            size = item.get("size")
            sha256 = item.get("sha256")
            if not isinstance(filename, str) or not isinstance(size, int) or not isinstance(sha256, str):
                continue
            archive = ArchivePin.from_mapping(
                {
                    "url": f"https://go.dev/dl/{filename}",
                    "size": size,
                    "sha256": sha256,
                    "sha1": None,
                },
                "go.archive",
            )
            return GoSelection(version=version, archive=archive)
    raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "stable linux-amd64 Go archive not found")


def select_adoptium(info: object, assets: object) -> JdkSelection:
    if not isinstance(info, dict) or not isinstance(info.get("most_recent_lts"), int):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Adoptium release info is invalid")
    feature = info["most_recent_lts"]
    if not isinstance(assets, list):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Adoptium assets are invalid")
    for item in assets:
        if not isinstance(item, dict):
            continue
        release_name = item.get("release_name")
        package = item.get("binary", {}).get("package") if isinstance(item.get("binary"), dict) else None
        if not isinstance(release_name, str) or not isinstance(package, dict):
            continue
        link, size, checksum = package.get("link"), package.get("size"), package.get("checksum")
        if not isinstance(link, str) or not isinstance(size, int) or not isinstance(checksum, str):
            continue
        version = release_name.removeprefix("jdk-")
        if not version.startswith(f"{feature}."):
            continue
        archive = ArchivePin.from_mapping(
            {"url": link, "size": size, "sha256": checksum, "sha1": None},
            "jdk.archive",
        )
        return JdkSelection(vendor="Eclipse Temurin", version=version, archive=archive)
    raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "latest Adoptium LTS asset not found")


def select_gomobile(latest: object, sumdb: str) -> GoModuleSelection:
    if not isinstance(latest, dict):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "gomobile metadata is invalid")
    version = latest.get("Version")
    origin = latest.get("Origin")
    if (
        not isinstance(version, str)
        or not isinstance(origin, dict)
        or origin.get("URL") != "https://github.com/sagernet/gomobile"
    ):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "gomobile origin is not reviewed")
    prefix = f"github.com/sagernet/gomobile {version} "
    matches = [line.removeprefix(prefix) for line in sumdb.splitlines() if line.startswith(prefix)]
    if len(matches) != 1 or not matches[0].startswith("h1:"):
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "gomobile module sum is missing or ambiguous")
    return GoModuleSelection(
        module="github.com/sagernet/gomobile",
        version=version,
        sum=matches[0],
    )


def parse_android_repository(encoded: bytes) -> AndroidSelection:
    if len(encoded) > 8 * 1024 * 1024:
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Android repository metadata exceeds size limit")
    try:
        root = ElementTree.fromstring(encoded)
    except ElementTree.ParseError as failure:
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Android repository metadata is invalid XML") from failure
    candidates: dict[str, list[AndroidPackageSelection]] = {
        "cmdline-tools;": [],
        "platforms;android-": [],
        "build-tools;": [],
        "ndk;": [],
    }
    for remote in root.iter():
        if not remote.tag.endswith("remotePackage"):
            continue
        package = remote.attrib.get("path", "")
        prefix = next((item for item in candidates if package.startswith(item)), None)
        if prefix is None or package.endswith(";latest") or _is_preview(package):
            continue
        channels = [
            child.attrib.get("ref")
            for child in remote
            if child.tag.endswith("channelRef")
        ]
        if any(channel != "channel-0" for channel in channels):
            continue
        archive = _linux_archive(remote)
        if archive is not None and not _is_preview(archive["url"]):
            candidates[prefix].append(
                AndroidPackageSelection(
                    package=package,
                    url=f"https://dl.google.com/android/repository/{archive['url']}",
                    size=int(archive["size"]),
                    sha1=archive["sha1"],
                )
            )
    selected: list[AndroidPackageSelection] = []
    for prefix in ("cmdline-tools;", "platforms;android-", "build-tools;", "ndk;"):
        values = candidates[prefix]
        if not values:
            raise ReleaseError("TOOLCHAIN_METADATA_INVALID", f"stable Android package missing: {prefix}")
        if prefix == "platforms;android-":
            api_23 = [item for item in values if item.package == "platforms;android-23"]
            if len(api_23) != 1:
                raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Android API 23 platform package is missing")
            selected.append(api_23[0])
        else:
            selected.append(max(values, key=lambda item: _version_key(item.package.removeprefix(prefix))))
    return AndroidSelection(*selected)


def _linux_archive(remote: ElementTree.Element) -> dict[str, str] | None:
    for archive in remote.iter():
        if not archive.tag.endswith("archive"):
            continue
        host = next((child.text for child in archive if child.tag.endswith("host-os")), None)
        if host not in (None, "linux"):
            continue
        complete = next((child for child in archive if child.tag.endswith("complete")), None)
        if complete is None:
            continue
        size = next((child.text for child in complete if child.tag.endswith("size")), None)
        url = next((child.text for child in complete if child.tag.endswith("url")), None)
        checksum = next((child for child in complete if child.tag.endswith("checksum")), None)
        if (
            size is None
            or not size.isdigit()
            or url is None
            or "/" in url
            or checksum is None
            or checksum.attrib.get("type") != "sha1"
            or checksum.text is None
            or re.fullmatch(r"[0-9a-f]{40}", checksum.text) is None
        ):
            raise ReleaseError("TOOLCHAIN_METADATA_INVALID", "Android archive metadata is invalid")
        return {"size": size, "url": url, "sha1": checksum.text}
    return None


def _version_key(version: str) -> tuple[int, ...]:
    if re.fullmatch(r"[0-9]+(?:\.[0-9]+)*", version) is None:
        raise ReleaseError("TOOLCHAIN_METADATA_INVALID", f"noncanonical stable Android version: {version}")
    return tuple(int(part) for part in version.split("."))


def _is_preview(package: str) -> bool:
    lowered = package.lower()
    return any(marker in lowered for marker in ("alpha", "beta", "rc", "preview", "canary"))


def _hash_archive(
    path: Path,
    url: str,
    expected_size: int | None,
    expected_sha1: str | None,
) -> ArchivePin:
    sha256 = hashlib.sha256()
    sha1 = hashlib.sha1(usedforsecurity=False)
    size = 0
    try:
        with path.open("rb") as source:
            while block := source.read(1024 * 1024):
                size += len(block)
                sha256.update(block)
                sha1.update(block)
    except OSError as failure:
        raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"cannot read cached archive: {path}") from failure
    if expected_size is not None and size != expected_size:
        raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"archive size mismatch: {url}")
    actual_sha1 = sha1.hexdigest()
    if expected_sha1 is not None and actual_sha1 != expected_sha1:
        raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"archive SHA-1 mismatch: {url}")
    return ArchivePin(url=url, size=size, sha256=sha256.hexdigest(), sha1=expected_sha1)


def _require_download_url(url: str) -> None:
    parsed = urlparse(url)
    if (
        parsed.scheme != "https"
        or parsed.hostname not in ALLOWED_DOWNLOAD_HOSTS
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
    ):
        raise ReleaseError("TOOLCHAIN_URL_INVALID", f"untrusted download URL: {url}")


def _require_sha256(identity: ArchivePin, expected: str | None) -> None:
    if expected is not None and identity.sha256 != expected:
        raise ReleaseError("TOOLCHAIN_ARCHIVE_INVALID", f"archive SHA-256 mismatch: {identity.url}")
