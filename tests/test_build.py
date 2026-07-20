import json
import os
from pathlib import Path
import unittest
from unittest import mock

from androidlibboxlite.build import (
    build_command,
    clean_build_environment,
    host_tool_environment,
    run_libbox_command,
)
from androidlibboxlite.lockfile import ReleaseLock
from tests.fixtures import release_lock_dict


class BuildCommandTest(unittest.TestCase):
    def setUp(self) -> None:
        self.lock = ReleaseLock.from_json(json.dumps(release_lock_dict()).encode("utf-8"))

    def test_build_command_preserves_reviewed_arguments(self) -> None:
        command = build_command(
            self.lock,
            Path("/tools/gomobile"),
            Path("/source"),
            Path("/output/libbox.aar"),
        )

        self.assertEqual(str(Path("/tools/gomobile")), command[0])
        self.assertEqual("android", command[command.index("-target") + 1])
        self.assertEqual("23", command[command.index("-androidapi") + 1])
        self.assertEqual("-javapkg=io.nekohasekai", command[command.index("-javapkg=io.nekohasekai")])
        self.assertEqual("-libname=box", command[command.index("-libname=box")])
        self.assertEqual(self.lock.libbox.ldflags, command[command.index("-ldflags") + 1])
        self.assertEqual(",".join(self.lock.libbox.tags), command[command.index("-tags") + 1])
        self.assertEqual("./experimental/libbox", command[-1])

    def test_build_environment_is_closed_and_reproducible(self) -> None:
        environment = clean_build_environment(
            self.lock,
            Path("/tools/go"),
            Path("/tools/jdk"),
            Path("/tools/sdk"),
            Path("/tools/ndk"),
            Path("/work"),
        )

        self.assertNotIn("USERPROFILE", environment)
        self.assertNotIn("ANDROID_SDK_HOME", environment)
        self.assertEqual(str(self.lock.source.commit_time), environment["SOURCE_DATE_EPOCH"])
        self.assertEqual("local", environment["GOTOOLCHAIN"])
        self.assertEqual("UTC", environment["TZ"])

    def test_host_go_tools_disable_cgo_without_changing_android_build_environment(self) -> None:
        android_environment = {"CGO_ENABLED": "1", "GOTOOLCHAIN": "local"}

        host_environment = host_tool_environment(android_environment)

        self.assertEqual("0", host_environment["CGO_ENABLED"])
        self.assertEqual("1", android_environment["CGO_ENABLED"])
        self.assertEqual("local", host_environment["GOTOOLCHAIN"])

    def test_libbox_build_streams_verbose_process_output(self) -> None:
        command = ("/tools/gomobile", "bind", "-v")
        source = Path("/source")
        environment = {"CGO_ENABLED": "1"}

        with mock.patch("androidlibboxlite.build.run_checked") as execute:
            run_libbox_command(command, source, environment)

        execute.assert_called_once_with(
            command,
            source,
            environment,
            90 * 60,
            "LIBBOX_BUILD_FAILED",
            stream_output=True,
        )


if __name__ == "__main__":
    unittest.main()
