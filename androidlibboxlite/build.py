from __future__ import annotations

import os
from pathlib import Path
import json

from dataclasses import dataclass

from .archive import safe_extract_tar_gz, safe_extract_zip
from .errors import ReleaseError
from .lockfile import AndroidPackagePin, ArchivePin, ReleaseLock
from .process import run_checked
from .toolchains import ArchiveCache


@dataclass(frozen=True)
class BuildOutputs:
    raw_aar: Path
    sources: Path
    go: Path


def build_command(
    lock: ReleaseLock,
    gomobile: Path,
    source: Path,
    output: Path,
) -> tuple[str, ...]:
    del source
    return (
        str(gomobile),
        "bind",
        "-v",
        "-o",
        str(output),
        "-target",
        "android",
        "-androidapi",
        str(lock.libbox.android_api),
        "-javapkg=io.nekohasekai",
        "-libname=box",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags",
        lock.libbox.ldflags,
        "-tags",
        ",".join(lock.libbox.tags),
        "./experimental/libbox",
    )


def clean_build_environment(
    lock: ReleaseLock,
    go_root: Path,
    jdk_root: Path,
    android_sdk: Path,
    android_ndk: Path,
    workspace: Path,
) -> dict[str, str]:
    path_separator = os.pathsep
    return {
        "ANDROID_HOME": str(android_sdk),
        "ANDROID_NDK_HOME": str(android_ndk),
        "ANDROID_SDK_ROOT": str(android_sdk),
        "CGO_ENABLED": "1",
        "GOBIN": str(workspace / "bin"),
        "GOCACHE": str(workspace / "go-build-cache"),
        "GOENV": "off",
        "GOMODCACHE": str(workspace / "go-module-cache"),
        "GOPATH": str(workspace / "gopath"),
        "GOPROXY": "https://proxy.golang.org",
        "GOSUMDB": "sum.golang.org",
        "GOTOOLCHAIN": "local",
        "HOME": str(workspace / "home"),
        "JAVA_HOME": str(jdk_root),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PATH": path_separator.join(
            (
                str(go_root / "bin"),
                str(jdk_root / "bin"),
                str(workspace / "bin"),
                "/usr/bin",
                "/bin",
            )
        ),
        "SOURCE_DATE_EPOCH": str(lock.source.commit_time),
        "TMPDIR": str(workspace / "tmp"),
        "TZ": "UTC",
    }


def host_tool_environment(android_environment: dict[str, str]) -> dict[str, str]:
    environment = dict(android_environment)
    environment["CGO_ENABLED"] = "0"
    return environment


def run_libbox_command(
    command: tuple[str, ...],
    source: Path,
    environment: dict[str, str],
) -> None:
    run_checked(
        command,
        source,
        environment,
        90 * 60,
        "LIBBOX_BUILD_FAILED",
        stream_output=True,
    )


def build_libbox(
    lock: ReleaseLock,
    workspace: Path,
    output: Path,
    cache_root: Path,
) -> BuildOutputs:
    if os.name == "nt":
        raise ReleaseError("BUILD_HOST_UNSUPPORTED", "libbox provider builds require Linux")
    workspace = workspace.resolve()
    output = output.resolve()
    if workspace.exists() or output.exists():
        raise ReleaseError("BUILD_DIRECTORY_INVALID", "workspace and output must not already exist")
    workspace.mkdir(parents=True)
    output.mkdir(parents=True)
    cache = ArchiveCache(cache_root)
    tools = workspace / "tools"
    tools.mkdir()

    go_root = _install_tar(cache, lock.toolchain.go.archive, workspace / "extract-go", tools / "go")
    jdk_root = _install_tar(cache, lock.toolchain.jdk.archive, workspace / "extract-jdk", tools / "jdk")
    android_sdk = tools / "android-sdk"
    android_sdk.mkdir()
    _install_android_package(
        cache,
        lock.toolchain.android.command_line_tools,
        workspace / "extract-command-line-tools",
        android_sdk / "cmdline-tools" / lock.toolchain.android.command_line_tools.package.split(";", 1)[1],
    )
    _install_android_package(
        cache,
        lock.toolchain.android.platform,
        workspace / "extract-platform",
        android_sdk / "platforms" / lock.toolchain.android.platform.package.split(";", 1)[1],
    )
    _install_android_package(
        cache,
        lock.toolchain.android.build_tools,
        workspace / "extract-build-tools",
        android_sdk / "build-tools" / lock.toolchain.android.build_tools.package.split(";", 1)[1],
    )
    android_ndk = _install_android_package(
        cache,
        lock.toolchain.android.ndk,
        workspace / "extract-ndk",
        android_sdk / "ndk" / lock.toolchain.android.ndk.package.split(";", 1)[1],
    )
    source = _install_zip(
        cache,
        lock.source.archive,
        workspace / "extract-source",
        workspace / "source",
    )
    for directory in (
        workspace / "bin",
        workspace / "go-build-cache",
        workspace / "go-module-cache",
        workspace / "gopath",
        workspace / "home",
        workspace / "tmp",
    ):
        directory.mkdir()

    go = go_root / "bin/go"
    java = jdk_root / "bin/java"
    clang = android_ndk / "toolchains/llvm/prebuilt/linux-x86_64/bin/clang"
    android_jar = android_sdk / "platforms/android-23/android.jar"
    for required in (go, java, clang, android_jar):
        if not required.is_file():
            raise ReleaseError("TOOLCHAIN_LAYOUT_INVALID", f"required toolchain file is missing: {required}")
    environment = clean_build_environment(lock, go_root, jdk_root, android_sdk, android_ndk, workspace)
    go_version = run_checked([str(go), "version"], workspace, environment, 30, "GO_VERSION_FAILED")
    if f" {lock.toolchain.go.version} " not in go_version.stdout:
        raise ReleaseError("TOOLCHAIN_IDENTITY_INVALID", go_version.stdout.strip())

    host_environment = host_tool_environment(environment)
    module = f"{lock.toolchain.gomobile.module}@{lock.toolchain.gomobile.version}"
    module_result = run_checked(
        [str(go), "mod", "download", "-json", module],
        workspace,
        host_environment,
        15 * 60,
        "GOMOBILE_DOWNLOAD_FAILED",
    )
    try:
        module_identity = json.loads(module_result.stdout)
    except json.JSONDecodeError as failure:
        raise ReleaseError("GOMOBILE_IDENTITY_INVALID", "go mod download did not return JSON") from failure
    if module_identity.get("Path") != lock.toolchain.gomobile.module or module_identity.get("Version") != lock.toolchain.gomobile.version or module_identity.get("Sum") != lock.toolchain.gomobile.sum:
        raise ReleaseError("GOMOBILE_IDENTITY_INVALID", "gomobile module identity differs from the lock")
    for command in ("gomobile", "gobind"):
        run_checked(
            [str(go), "install", f"{lock.toolchain.gomobile.module}/cmd/{command}@{lock.toolchain.gomobile.version}"],
            workspace,
            host_environment,
            30 * 60,
            "GOMOBILE_INSTALL_FAILED",
        )
    gomobile = workspace / "bin/gomobile"
    gobind = workspace / "bin/gobind"
    if not gomobile.is_file() or not gobind.is_file():
        raise ReleaseError("GOMOBILE_IDENTITY_INVALID", "gomobile or gobind binary is missing")
    run_checked([str(gomobile), "init"], workspace, host_environment, 10 * 60, "GOMOBILE_INIT_FAILED")
    raw_aar = output / "libbox.aar"
    command = build_command(lock, gomobile, source, raw_aar)
    run_libbox_command(command, source, environment)
    sources = output / "libbox-sources.jar"
    if not raw_aar.is_file() or not sources.is_file():
        raise ReleaseError("LIBBOX_OUTPUT_INVALID", "gomobile did not create both release inputs")
    return BuildOutputs(raw_aar=raw_aar, sources=sources, go=go)


def _install_tar(cache: ArchiveCache, pin: ArchivePin, extraction: Path, destination: Path) -> Path:
    archive = cache.materialize(pin)
    safe_extract_tar_gz(archive, extraction)
    return _move_single_root(extraction, destination)


def _install_zip(cache: ArchiveCache, pin: ArchivePin, extraction: Path, destination: Path) -> Path:
    archive = cache.materialize(pin)
    safe_extract_zip(archive, extraction)
    return _move_single_root(extraction, destination)


def _install_android_package(
    cache: ArchiveCache,
    package: AndroidPackagePin,
    extraction: Path,
    destination: Path,
) -> Path:
    return _install_zip(cache, package.archive, extraction, destination)


def _move_single_root(extraction: Path, destination: Path) -> Path:
    entries = list(extraction.iterdir())
    if len(entries) != 1 or not entries[0].is_dir() or destination.exists():
        raise ReleaseError("TOOLCHAIN_LAYOUT_INVALID", f"archive does not contain one top-level directory: {extraction}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    os.replace(entries[0], destination)
    extraction.rmdir()
    return destination
