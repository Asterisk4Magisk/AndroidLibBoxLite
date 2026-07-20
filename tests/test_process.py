import io
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

from androidlibboxlite.errors import ReleaseError
from androidlibboxlite.process import run


class ProcessTest(unittest.TestCase):
    def test_preserves_ldflags_as_one_argument_and_closes_environment(self) -> None:
        script = "import json,os,sys;print(json.dumps({'argv':sys.argv[1:],'leak':os.getenv('ANDROID_HOME')}))"

        result = run(
            [sys.executable, "-c", script, "-ldflags", "-X a=b -s -w"],
            Path.cwd(),
            {"PATH": os.environ.get("PATH", ""), "SYSTEMROOT": os.environ.get("SYSTEMROOT", "")},
            timeout=10,
        )

        self.assertEqual(0, result.exit_code)
        payload = json.loads(result.stdout)
        self.assertEqual(["-ldflags", "-X a=b -s -w"], payload["argv"])
        self.assertIsNone(payload["leak"])

    def test_streams_live_output_while_preserving_captured_result(self) -> None:
        stdout = io.StringIO()
        stderr = io.StringIO()
        completed = threading.Event()
        results = []
        failures = []

        def invoke() -> None:
            try:
                with (
                    mock.patch("androidlibboxlite.process.sys.stdout", stdout),
                    mock.patch("androidlibboxlite.process.sys.stderr", stderr),
                ):
                    results.append(
                        run(
                            [
                                sys.executable,
                                "-c",
                                (
                                    "import sys,time;"
                                    "print('READY',flush=True);"
                                    "print('DETAIL',file=sys.stderr,flush=True);"
                                    "time.sleep(1);"
                                    "print('DONE',flush=True)"
                                ),
                            ],
                            Path.cwd(),
                            {
                                "PATH": os.environ.get("PATH", ""),
                                "SYSTEMROOT": os.environ.get("SYSTEMROOT", ""),
                            },
                            timeout=10,
                            stream_output=True,
                        )
                    )
            except BaseException as failure:
                failures.append(failure)
            finally:
                completed.set()

        worker = threading.Thread(target=invoke)
        worker.start()
        deadline = time.monotonic() + 0.75
        while "READY" not in stdout.getvalue() and time.monotonic() < deadline:
            time.sleep(0.01)

        self.assertIn("READY", stdout.getvalue())
        self.assertIn("DETAIL", stderr.getvalue())
        self.assertFalse(completed.is_set())
        worker.join(timeout=5)
        self.assertFalse(worker.is_alive())
        self.assertEqual([], failures)
        self.assertEqual("READY\nDONE\n", results[0].stdout.replace("\r\n", "\n"))
        self.assertEqual("DETAIL\n", results[0].stderr.replace("\r\n", "\n"))

    def test_timeout_uses_stable_error(self) -> None:
        with self.assertRaises(ReleaseError) as caught:
            run(
                [sys.executable, "-c", "import time;time.sleep(30)"],
                Path.cwd(),
                {"PATH": os.environ.get("PATH", ""), "SYSTEMROOT": os.environ.get("SYSTEMROOT", "")},
                timeout=1,
            )

        self.assertEqual("PROCESS_TIMEOUT", caught.exception.code)


if __name__ == "__main__":
    unittest.main()
