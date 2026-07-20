from __future__ import annotations

from dataclasses import dataclass
import json
import re
from typing import Any, Mapping
from urllib.parse import urlparse

from .errors import ReleaseError
from .semver import SemVer


LOCK_SCHEMA = 1
REQUIRED_ABIS = ("arm64-v8a", "armeabi-v7a", "x86", "x86_64")
REQUIRED_TAGS = (
    "with_gvisor",
    "with_quic",
    "with_dhcp",
    "with_wireguard",
    "with_utls",
    "with_clash_api",
    "with_tailscale",
    "with_naive_outbound",
    "with_openvpn",
    "with_openconnect",
    "badlinkname",
    "tfogo_checklinkname0",
    "ts_omit_logtail",
    "ts_omit_ssh",
    "ts_omit_drive",
    "ts_omit_taildrop",
    "ts_omit_webclient",
    "ts_omit_doctor",
    "ts_omit_capture",
    "ts_omit_kube",
    "ts_omit_aws",
    "ts_omit_synology",
    "ts_omit_bird",
)
FORBIDDEN_TAGS = frozenset({"with_embedded_tor", "with_usbip"})
ALLOWED_DOWNLOAD_HOSTS = frozenset(
    {
        "api.adoptium.net",
        "api.github.com",
        "codeload.github.com",
        "dl.google.com",
        "github.com",
        "go.dev",
        "proxy.golang.org",
        "sum.golang.org",
    }
)
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_SHA1 = re.compile(r"^[0-9a-f]{40}$")
_GO_SUM = re.compile(r"^h1:[A-Za-z0-9+/]{43}=$")
_MODULE_VERSION = re.compile(r"^v[0-9][0-9A-Za-z.+-]{0,127}$")
_MAX_LOCK_BYTES = 1024 * 1024


def libbox_ldflags(tag: str) -> str:
    version = SemVer.parse(tag).tag.removeprefix("v")
    return (
        f"-X github.com/sagernet/sing-box/constant.Version={version} "
        "-X internal/godebug.defaultGODEBUG=multipathtcp=0 "
        "-checklinkname=0 -s -w -buildid="
    )


@dataclass(frozen=True)
class ArchivePin:
    url: str
    size: int
    sha256: str
    sha1: str | None

    @classmethod
    def from_mapping(cls, value: object, path: str) -> "ArchivePin":
        mapping = _mapping(value, path)
        _exact_keys(mapping, {"url", "size", "sha256", "sha1"}, path)
        url = _string(mapping["url"], f"{path}.url")
        parsed = urlparse(url)
        if (
            parsed.scheme != "https"
            or parsed.hostname not in ALLOWED_DOWNLOAD_HOSTS
            or parsed.username is not None
            or parsed.password is not None
            or parsed.fragment
        ):
            _schema_error(f"{path}.url is not an allowed HTTPS URL")
        size = mapping["size"]
        if not isinstance(size, int) or isinstance(size, bool) or size <= 0:
            _schema_error(f"{path}.size must be a positive integer")
        sha256 = _string(mapping["sha256"], f"{path}.sha256").lower()
        if _SHA256.fullmatch(sha256) is None:
            _schema_error(f"{path}.sha256 is invalid")
        raw_sha1 = mapping["sha1"]
        sha1: str | None
        if raw_sha1 is None:
            sha1 = None
        else:
            sha1 = _string(raw_sha1, f"{path}.sha1").lower()
            if _SHA1.fullmatch(sha1) is None:
                _schema_error(f"{path}.sha1 is invalid")
        return cls(url=url, size=size, sha256=sha256, sha1=sha1)

    def to_dict(self) -> dict[str, object]:
        return {"url": self.url, "size": self.size, "sha256": self.sha256, "sha1": self.sha1}


@dataclass(frozen=True)
class SourcePin:
    repository: str
    tag: str
    commit: str
    commit_time: int
    archive: ArchivePin


@dataclass(frozen=True)
class GoPin:
    version: str
    archive: ArchivePin


@dataclass(frozen=True)
class GoModulePin:
    module: str
    version: str
    sum: str


@dataclass(frozen=True)
class JdkPin:
    vendor: str
    version: str
    archive: ArchivePin


@dataclass(frozen=True)
class AndroidPackagePin:
    package: str
    archive: ArchivePin


@dataclass(frozen=True)
class AndroidPin:
    repository: str
    command_line_tools: AndroidPackagePin
    platform: AndroidPackagePin
    build_tools: AndroidPackagePin
    ndk: AndroidPackagePin


@dataclass(frozen=True)
class ToolchainPin:
    go: GoPin
    gomobile: GoModulePin
    jdk: JdkPin
    android: AndroidPin


@dataclass(frozen=True)
class LibboxPin:
    android_api: int
    abis: tuple[str, ...]
    tags: tuple[str, ...]
    ldflags: str


@dataclass(frozen=True)
class ReleaseLock:
    schema: int
    source: SourcePin
    toolchain: ToolchainPin
    libbox: LibboxPin
    workflow_commit: str

    @classmethod
    def from_json(cls, encoded: bytes) -> "ReleaseLock":
        if len(encoded) > _MAX_LOCK_BYTES:
            _schema_error("lock exceeds size limit")

        def reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
            result: dict[str, Any] = {}
            for key, value in pairs:
                if key in result:
                    _schema_error(f"duplicate field: {key}")
                result[key] = value
            return result

        try:
            raw = json.loads(encoded.decode("utf-8"), object_pairs_hook=reject_duplicates)
        except (UnicodeDecodeError, json.JSONDecodeError) as failure:
            raise ReleaseError("LOCK_SCHEMA_INVALID", "lock is not UTF-8 JSON") from failure
        root = _mapping(raw, "lock")
        _exact_keys(root, {"schema", "source", "toolchain", "libbox", "workflowCommit"}, "lock")
        if root["schema"] != LOCK_SCHEMA:
            _schema_error("unsupported schema")

        source_raw = _mapping(root["source"], "source")
        _exact_keys(source_raw, {"repository", "tag", "commit", "commitTime", "archive"}, "source")
        repository = _string(source_raw["repository"], "source.repository")
        if repository != "SagerNet/sing-box":
            _policy_error("source repository must be SagerNet/sing-box")
        tag = _string(source_raw["tag"], "source.tag")
        SemVer.parse(tag)
        commit = _sha1(source_raw["commit"], "source.commit")
        commit_time = source_raw["commitTime"]
        if not isinstance(commit_time, int) or isinstance(commit_time, bool) or not 1_262_304_000 <= commit_time <= 4_102_444_800:
            _schema_error("source.commitTime is outside the reviewed range")
        source = SourcePin(
            repository=repository,
            tag=tag,
            commit=commit,
            commit_time=commit_time,
            archive=ArchivePin.from_mapping(source_raw["archive"], "source.archive"),
        )

        toolchain_raw = _mapping(root["toolchain"], "toolchain")
        _exact_keys(toolchain_raw, {"go", "gomobile", "jdk", "android"}, "toolchain")
        go_raw = _mapping(toolchain_raw["go"], "toolchain.go")
        _exact_keys(go_raw, {"version", "archive"}, "toolchain.go")
        go = GoPin(
            version=_string(go_raw["version"], "toolchain.go.version"),
            archive=ArchivePin.from_mapping(go_raw["archive"], "toolchain.go.archive"),
        )
        if re.fullmatch(r"go[1-9][0-9]*\.[0-9]+\.[0-9]+", go.version) is None:
            _schema_error("toolchain.go.version is invalid")

        mobile_raw = _mapping(toolchain_raw["gomobile"], "toolchain.gomobile")
        _exact_keys(mobile_raw, {"module", "version", "sum"}, "toolchain.gomobile")
        module = _string(mobile_raw["module"], "toolchain.gomobile.module")
        version = _string(mobile_raw["version"], "toolchain.gomobile.version")
        module_sum = _string(mobile_raw["sum"], "toolchain.gomobile.sum")
        if module != "github.com/sagernet/gomobile":
            _policy_error("gomobile module is not the SagerNet fork")
        if _MODULE_VERSION.fullmatch(version) is None or _GO_SUM.fullmatch(module_sum) is None:
            _schema_error("gomobile version or sum is invalid")
        gomobile = GoModulePin(module=module, version=version, sum=module_sum)

        jdk_raw = _mapping(toolchain_raw["jdk"], "toolchain.jdk")
        _exact_keys(jdk_raw, {"vendor", "version", "archive"}, "toolchain.jdk")
        jdk = JdkPin(
            vendor=_string(jdk_raw["vendor"], "toolchain.jdk.vendor"),
            version=_string(jdk_raw["version"], "toolchain.jdk.version"),
            archive=ArchivePin.from_mapping(jdk_raw["archive"], "toolchain.jdk.archive"),
        )
        if jdk.vendor != "Eclipse Temurin":
            _policy_error("JDK vendor must be Eclipse Temurin")

        android_raw = _mapping(toolchain_raw["android"], "toolchain.android")
        _exact_keys(
            android_raw,
            {"repository", "commandLineTools", "platform", "buildTools", "ndk"},
            "toolchain.android",
        )
        android_repository = _string(android_raw["repository"], "toolchain.android.repository")
        if android_repository != "https://dl.google.com/android/repository/repository2-3.xml":
            _policy_error("Android repository URL is not reviewed")
        android = AndroidPin(
            repository=android_repository,
            command_line_tools=_android_package(
                android_raw["commandLineTools"], "toolchain.android.commandLineTools", "cmdline-tools;"
            ),
            platform=_android_package(android_raw["platform"], "toolchain.android.platform", "platforms;android-"),
            build_tools=_android_package(
                android_raw["buildTools"], "toolchain.android.buildTools", "build-tools;"
            ),
            ndk=_android_package(android_raw["ndk"], "toolchain.android.ndk", "ndk;"),
        )

        libbox_raw = _mapping(root["libbox"], "libbox")
        _exact_keys(libbox_raw, {"androidApi", "abis", "tags", "ldflags"}, "libbox")
        android_api = libbox_raw["androidApi"]
        if android_api != 23:
            _policy_error("Android API must be 23")
        abis = _string_tuple(libbox_raw["abis"], "libbox.abis")
        tags = _string_tuple(libbox_raw["tags"], "libbox.tags")
        ldflags = _string(libbox_raw["ldflags"], "libbox.ldflags")
        if abis != REQUIRED_ABIS:
            _policy_error("Android ABI set or order changed")
        if tags != REQUIRED_TAGS or FORBIDDEN_TAGS.intersection(tags):
            _policy_error("libbox build tags changed or contain a forbidden feature")
        if ldflags != libbox_ldflags(tag):
            _policy_error("libbox linker flags changed")
        libbox = LibboxPin(android_api=android_api, abis=abis, tags=tags, ldflags=ldflags)

        workflow_commit = _sha1(root["workflowCommit"], "workflowCommit")
        return cls(
            schema=LOCK_SCHEMA,
            source=source,
            toolchain=ToolchainPin(go=go, gomobile=gomobile, jdk=jdk, android=android),
            libbox=libbox,
            workflow_commit=workflow_commit,
        )

    def to_canonical_json(self) -> bytes:
        return (json.dumps(self.to_dict(), ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")

    def to_dict(self) -> dict[str, object]:
        def package(value: AndroidPackagePin) -> dict[str, object]:
            return {"package": value.package, "archive": value.archive.to_dict()}

        return {
            "schema": self.schema,
            "source": {
                "repository": self.source.repository,
                "tag": self.source.tag,
                "commit": self.source.commit,
                "commitTime": self.source.commit_time,
                "archive": self.source.archive.to_dict(),
            },
            "toolchain": {
                "go": {"version": self.toolchain.go.version, "archive": self.toolchain.go.archive.to_dict()},
                "gomobile": {
                    "module": self.toolchain.gomobile.module,
                    "version": self.toolchain.gomobile.version,
                    "sum": self.toolchain.gomobile.sum,
                },
                "jdk": {
                    "vendor": self.toolchain.jdk.vendor,
                    "version": self.toolchain.jdk.version,
                    "archive": self.toolchain.jdk.archive.to_dict(),
                },
                "android": {
                    "repository": self.toolchain.android.repository,
                    "commandLineTools": package(self.toolchain.android.command_line_tools),
                    "platform": package(self.toolchain.android.platform),
                    "buildTools": package(self.toolchain.android.build_tools),
                    "ndk": package(self.toolchain.android.ndk),
                },
            },
            "libbox": {
                "androidApi": self.libbox.android_api,
                "abis": list(self.libbox.abis),
                "tags": list(self.libbox.tags),
                "ldflags": self.libbox.ldflags,
            },
            "workflowCommit": self.workflow_commit,
        }


def _mapping(value: object, path: str) -> Mapping[str, Any]:
    if not isinstance(value, dict) or not all(isinstance(key, str) for key in value):
        _schema_error(f"{path} must be an object")
    return value


def _exact_keys(mapping: Mapping[str, Any], expected: set[str], path: str) -> None:
    actual = set(mapping)
    if actual != expected:
        _schema_error(f"{path} fields differ: expected {sorted(expected)}, got {sorted(actual)}")


def _string(value: object, path: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 4096 or "\x00" in value:
        _schema_error(f"{path} must be a bounded non-empty string")
    return value


def _string_tuple(value: object, path: str) -> tuple[str, ...]:
    if not isinstance(value, list):
        _schema_error(f"{path} must be an array")
    result = tuple(_string(item, f"{path}[]") for item in value)
    if len(result) != len(set(result)):
        _schema_error(f"{path} contains duplicates")
    return result


def _sha1(value: object, path: str) -> str:
    result = _string(value, path).lower()
    if _SHA1.fullmatch(result) is None:
        _schema_error(f"{path} is not a complete SHA-1 object id")
    return result


def _android_package(value: object, path: str, prefix: str) -> AndroidPackagePin:
    mapping = _mapping(value, path)
    _exact_keys(mapping, {"package", "archive"}, path)
    package = _string(mapping["package"], f"{path}.package")
    if not package.startswith(prefix) or any(marker in package.lower() for marker in ("alpha", "beta", "rc")):
        _policy_error(f"{path}.package is not a stable reviewed package")
    return AndroidPackagePin(package=package, archive=ArchivePin.from_mapping(mapping["archive"], f"{path}.archive"))


def _schema_error(message: str) -> None:
    raise ReleaseError("LOCK_SCHEMA_INVALID", message)


def _policy_error(message: str) -> None:
    raise ReleaseError("LOCK_POLICY_INVALID", message)
