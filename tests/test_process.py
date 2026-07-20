import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

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
