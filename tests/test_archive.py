from pathlib import Path
import io
import os
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

from androidlibboxlite.archive import (
    normalize_aar,
    safe_extract_tar_gz,
    safe_extract_zip,
    validated_entries,
)
from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.lockfile import REQUIRED_ABIS


def write_aar(path: Path, reverse: bool) -> None:
    entries = [("AndroidManifest.xml", b"manifest"), ("classes.jar", b"classes")]
    entries.extend((f"jni/{abi}/libbox.so", abi.encode("ascii")) for abi in REQUIRED_ABIS)
    if reverse:
        entries.reverse()
    with zipfile.ZipFile(path, "w") as output:
        for index, (name, content) in enumerate(entries):
            info = zipfile.ZipInfo(name, date_time=(2025, 1, index + 1, 0, 0, 0))
            info.external_attr = (0o100600 + index) << 16
            output.writestr(info, content, compress_type=zipfile.ZIP_STORED)


class ArchiveTest(unittest.TestCase):
    def test_normalized_aar_is_byte_identical(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_aar(root / "first-raw.aar", reverse=False)
            write_aar(root / "second-raw.aar", reverse=True)

            normalize_aar(root / "first-raw.aar", root / "first.aar", REQUIRED_ABIS)
            normalize_aar(root / "second-raw.aar", root / "second.aar", REQUIRED_ABIS)

            self.assertEqual((root / "first.aar").read_bytes(), (root / "second.aar").read_bytes())
            with zipfile.ZipFile(root / "first.aar") as archive:
                self.assertEqual(sorted(archive.namelist()), archive.namelist())
                self.assertTrue(all(item.date_time == (1980, 1, 1, 0, 0, 0) for item in archive.infolist()))

    def test_rejects_traversal_duplicate_case_collision_and_missing_abi(self) -> None:
        cases = {
            "traversal": [('../escape', b'x')],
            "case": [('classes.jar', b'a'), ('CLASSES.JAR', b'b')],
            "missing": [(f"jni/{abi}/libbox.so", b'x') for abi in REQUIRED_ABIS[:-1]],
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, entries in cases.items():
                source = root / f"{name}.aar"
                with zipfile.ZipFile(source, "w") as output:
                    for entry, content in entries:
                        output.writestr(entry, content)
                with self.subTest(name=name), self.assertRaises(ReleaseError) as caught:
                    normalize_aar(source, root / f"{name}-normalized.aar", REQUIRED_ABIS)
                self.assertEqual("ARCHIVE_INVALID", caught.exception.code)

    def test_safe_zip_materializes_reviewed_internal_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "symlink.zip"
            with zipfile.ZipFile(source, "w") as output:
                target = zipfile.ZipInfo("tool/bin/run")
                target.create_system = 3
                target.external_attr = 0o100755 << 16
                output.writestr(target, b"#!/bin/sh\n")
                info = zipfile.ZipInfo("tool/lib/run")
                info.create_system = 3
                info.external_attr = 0o120777 << 16
                output.writestr(info, "../bin/run")

            destination = root / "output"
            safe_extract_zip(source, destination)

            self.assertEqual(b"#!/bin/sh\n", (destination / "tool/lib/run").read_bytes())
            self.assertFalse((destination / "tool/lib/run").is_symlink())

    def test_safe_zip_rejects_link_target_outside_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "escape.zip"
            with zipfile.ZipFile(source, "w") as output:
                info = zipfile.ZipInfo("tool/link")
                info.create_system = 3
                info.external_attr = 0o120777 << 16
                output.writestr(info, "../../escape")

            with self.assertRaises(ReleaseError) as caught:
                safe_extract_zip(source, root / "output")

            self.assertEqual("ARCHIVE_INVALID", caught.exception.code)

    def test_safe_zip_enforces_toolchain_expansion_limit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "large.zip"
            with zipfile.ZipFile(source, "w") as output:
                output.writestr("tool/file", b"12345")

            with mock.patch("androidlibboxlite.archive._MAX_TOOLCHAIN_TOTAL_BYTES", 4):
                with self.assertRaises(ReleaseError) as caught:
                    safe_extract_zip(source, root / "output")

            self.assertEqual("ARCHIVE_INVALID", caught.exception.code)

    def test_linux_toolchain_validation_accepts_exact_case_distinct_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "linux-headers.zip"
            with zipfile.ZipFile(source, "w") as output:
                output.writestr("include/xt_rateest.h", b"match")
                output.writestr("include/xt_RATEEST.h", b"target")

            with zipfile.ZipFile(source) as archive:
                entries = validated_entries(archive, allow_case_collisions=True)

            self.assertEqual(
                ["include/xt_rateest.h", "include/xt_RATEEST.h"],
                [entry.filename for entry in entries],
            )

    def test_safe_tar_extract_preserves_reviewed_executable_bit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "tool.tar.gz"
            with tarfile.open(source, "w:gz") as archive:
                directory_info = tarfile.TarInfo("tool/bin")
                directory_info.type = tarfile.DIRTYPE
                directory_info.mode = 0o755
                archive.addfile(directory_info)
                content = b"#!/bin/sh\n"
                file_info = tarfile.TarInfo("tool/bin/run")
                file_info.size = len(content)
                file_info.mode = 0o755
                archive.addfile(file_info, io.BytesIO(content))
                link_info = tarfile.TarInfo("tool/legal/COPY")
                link_info.type = tarfile.SYMTYPE
                link_info.linkname = "../bin/run"
                archive.addfile(link_info)

            destination = root / "output"
            safe_extract_tar_gz(source, destination)

            self.assertEqual(content, (destination / "tool/bin/run").read_bytes())
            self.assertEqual(content, (destination / "tool/legal/COPY").read_bytes())
            self.assertFalse((destination / "tool/legal/COPY").is_symlink())
            if os.name != "nt":
                self.assertTrue((destination / "tool/bin/run").stat().st_mode & 0o100)

    def test_safe_tar_rejects_link_target_outside_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "escape.tar.gz"
            with tarfile.open(source, "w:gz") as archive:
                link_info = tarfile.TarInfo("tool/link")
                link_info.type = tarfile.SYMTYPE
                link_info.linkname = "../../escape"
                archive.addfile(link_info)

            with self.assertRaises(ReleaseError) as caught:
                safe_extract_tar_gz(source, root / "output")

            self.assertEqual("ARCHIVE_INVALID", caught.exception.code)


if __name__ == "__main__":
    unittest.main()
