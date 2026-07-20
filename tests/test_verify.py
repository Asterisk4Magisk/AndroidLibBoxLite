from __future__ import annotations

import io
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import REQUIRED_ABIS, ReleaseLock
from androidlibboxlite.verify import EXPECTED_ELF_MACHINE, verify_release
from tests.fixtures import release_lock_dict


def nested_zip(entries: list[str]) -> bytes:
    target = io.BytesIO()
    with zipfile.ZipFile(target, "w") as archive:
        for entry in entries:
            archive.writestr(entry, b"content")
    return target.getvalue()


def elf(machine: int, version: str) -> bytes:
    value = bytearray(128)
    value[:4] = b"\x7fELF"
    value[4] = 2
    value[5] = 1
    value[18:20] = machine.to_bytes(2, "little")
    value[32 : 32 + len(version)] = version.encode("ascii")
    return bytes(value)


def build_info(lock: ReleaseLock, abi: str, forbidden_module: bool = False) -> str:
    go_arch = {
        "arm64-v8a": "arm64",
        "armeabi-v7a": "arm",
        "x86": "386",
        "x86_64": "amd64",
    }[abi]
    lines = [
        f"file: {lock.toolchain.go.version}",
        "\tpath\tgithub.com/sagernet/sing-box/build/test/libbox",
        f"\tbuild\t-tags={','.join(lock.libbox.tags)}",
        "\tbuild\t-trimpath=true",
        "\tbuild\tCGO_ENABLED=1",
        f"\tbuild\tGOARCH={go_arch}",
        "\tbuild\tGOOS=android",
    ]
    if forbidden_module:
        lines.insert(2, "\tdep\tgithub.com/sagernet/sing-usbip\tv0.0.0")
    return "\n".join(lines)


class VerifyReleaseTest(unittest.TestCase):
    def setUp(self) -> None:
        self.lock = ReleaseLock.from_json(json.dumps(release_lock_dict()).encode("utf-8"))

    def _write_fixture(self, root: Path) -> tuple[Path, Path]:
        aar = root / "libbox.aar"
        classes = nested_zip(
            [
                "io/nekohasekai/libbox/Libbox.class",
                "io/nekohasekai/libbox/SetupOptions.class",
            ]
        )
        with zipfile.ZipFile(aar, "w") as output:
            output.writestr("AndroidManifest.xml", b"manifest")
            output.writestr("classes.jar", classes)
            for abi in REQUIRED_ABIS:
                output.writestr(
                    f"jni/{abi}/libbox.so",
                    elf(EXPECTED_ELF_MACHINE[abi], self.lock.source.tag.removeprefix("v")),
                )
        sources = root / "libbox-sources.jar"
        with zipfile.ZipFile(sources, "w") as output:
            output.writestr("io/nekohasekai/libbox/Libbox.java", b"class Libbox {}")
            output.writestr("io/nekohasekai/libbox/SetupOptions.java", b"class SetupOptions {}")
        return aar, sources

    def test_verifies_classes_elf_build_info_and_no_embedded_components(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            aar, sources = self._write_fixture(Path(directory))

            report = verify_release(
                self.lock,
                aar,
                sources,
                lambda path, abi: build_info(self.lock, abi),
            )

            self.assertEqual(REQUIRED_ABIS, tuple(report.abis))
            self.assertEqual(EXPECTED_ELF_MACHINE["arm64-v8a"], report.abis["arm64-v8a"].machine)

    def test_rejects_usbip_module_in_compiled_build(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            aar, sources = self._write_fixture(Path(directory))

            with self.assertRaises(ReleaseError) as caught:
                verify_release(
                    self.lock,
                    aar,
                    sources,
                    lambda path, abi: build_info(self.lock, abi, forbidden_module=abi == "x86_64"),
                )

            self.assertEqual("ARTIFACT_POLICY_INVALID", caught.exception.code)


if __name__ == "__main__":
    unittest.main()
