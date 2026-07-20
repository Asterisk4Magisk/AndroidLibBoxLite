import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from androidlibboxlite.toolchains import (
    ArchiveCache,
    parse_android_repository,
    select_adoptium,
    select_go,
    select_gomobile,
)


class FakeDownload:
    def __init__(self, content: bytes) -> None:
        self.content = content
        self.offset = 0
        self.headers = {"Content-Length": str(len(content))}

    def __enter__(self) -> "FakeDownload":
        return self

    def __exit__(self, *args: object) -> None:
        return None

    def read(self, size: int) -> bytes:
        block = self.content[self.offset : self.offset + size]
        self.offset += len(block)
        return block


class ToolchainSelectionTest(unittest.TestCase):
    def test_selects_latest_stable_linux_go_archive(self) -> None:
        payload = [
            {
                "version": "go1.26.5",
                "stable": True,
                "files": [
                    {
                        "filename": "go1.26.5.linux-amd64.tar.gz",
                        "os": "linux",
                        "arch": "amd64",
                        "kind": "archive",
                        "sha256": "a" * 64,
                        "size": 42,
                    }
                ],
            },
            {"version": "go1.27rc1", "stable": False, "files": []},
        ]

        selected = select_go(payload)

        self.assertEqual("go1.26.5", selected.version)
        self.assertEqual("https://go.dev/dl/go1.26.5.linux-amd64.tar.gz", selected.archive.url)

    def test_selects_latest_lts_adoptium_ga(self) -> None:
        selected = select_adoptium(
            {"most_recent_lts": 25},
            [
                {
                    "release_name": "jdk-25.0.3+9",
                    "binary": {
                        "package": {
                            "link": "https://github.com/adoptium/temurin25-binaries/releases/download/a/jdk.tar.gz",
                            "size": 100,
                            "checksum": "b" * 64,
                        }
                    },
                }
            ],
        )

        self.assertEqual("25.0.3+9", selected.version)
        self.assertEqual("Eclipse Temurin", selected.vendor)

    def test_selects_sagernet_gomobile_version_and_sum(self) -> None:
        selected = select_gomobile(
            {"Version": "v0.1.13", "Origin": {"URL": "https://github.com/sagernet/gomobile"}},
            "555\ngithub.com/sagernet/gomobile v0.1.13 h1:abc=\n",
        )

        self.assertEqual("v0.1.13", selected.version)
        self.assertEqual("h1:abc=", selected.sum)

    def test_selects_latest_stable_android_packages(self) -> None:
        xml = b"""<?xml version='1.0' encoding='utf-8'?>
        <sdk:sdk-repository xmlns:sdk='http://schemas.android.com/sdk/android/repo/repository2/03'>
          <remotePackage path='cmdline-tools;22.0'><channelRef ref='channel-0'/><revision><major>22</major></revision><archives><archive><host-os>linux</host-os><complete><size>10</size><checksum type='sha1'>1111111111111111111111111111111111111111</checksum><url>cmd.zip</url></complete></archive></archives></remotePackage>
          <remotePackage path='platforms;android-23'><channelRef ref='channel-0'/><revision><major>23</major></revision><archives><archive><complete><size>11</size><checksum type='sha1'>2222222222222222222222222222222222222222</checksum><url>platform.zip</url></complete></archive></archives></remotePackage>
          <remotePackage path='build-tools;37.0.0'><channelRef ref='channel-0'/><revision><major>37</major></revision><archives><archive><host-os>linux</host-os><complete><size>12</size><checksum type='sha1'>3333333333333333333333333333333333333333</checksum><url>build.zip</url></complete></archive></archives></remotePackage>
          <remotePackage path='ndk;29.0.14206865'><channelRef ref='channel-0'/><revision><major>29</major></revision><archives><archive><host-os>linux</host-os><complete><size>13</size><checksum type='sha1'>4444444444444444444444444444444444444444</checksum><url>ndk.zip</url></complete></archive></archives></remotePackage>
          <remotePackage path='ndk;30.0.15729638'><channelRef ref='channel-0'/><revision><major>30</major></revision><archives><archive><host-os>linux</host-os><complete><size>15</size><checksum type='sha1'>6666666666666666666666666666666666666666</checksum><url>android-ndk-r30-beta2-linux.zip</url></complete></archive></archives></remotePackage>
          <remotePackage path='ndk;30.0.1-beta1'><channelRef ref='channel-1'/><revision><major>30</major></revision><archives><archive><host-os>linux</host-os><complete><size>14</size><checksum type='sha1'>5555555555555555555555555555555555555555</checksum><url>ndk-beta.zip</url></complete></archive></archives></remotePackage>
        </sdk:sdk-repository>"""

        selected = parse_android_repository(xml)

        self.assertEqual("cmdline-tools;22.0", selected.command_line_tools.package)
        self.assertEqual("platforms;android-23", selected.platform.package)
        self.assertEqual("build-tools;37.0.0", selected.build_tools.package)
        self.assertEqual("ndk;29.0.14206865", selected.ndk.package)

    def test_archive_cache_replaces_a_truncated_cached_download(self) -> None:
        url = "https://go.dev/dl/test.tar.gz"
        content = b"complete"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cache = ArchiveCache(root)
            identity = hashlib.sha256(url.encode("utf-8")).hexdigest()
            cached = root / f"{identity}.gz"
            cached.write_bytes(b"truncated")
            with patch("urllib.request.urlopen", return_value=FakeDownload(content)) as download:
                pin = cache.pin(url, len(content), None)

            self.assertEqual(content, cached.read_bytes())
            self.assertEqual(hashlib.sha256(content).hexdigest(), pin.sha256)
            self.assertEqual(1, download.call_count)


if __name__ == "__main__":
    unittest.main()
