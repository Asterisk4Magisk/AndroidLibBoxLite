from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github/workflows"
PINNED_USE = re.compile(
    r"^\s*uses:\s*[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}\s*$",
    re.MULTILINE,
)


class WorkflowContractTest(unittest.TestCase):
    def test_all_external_actions_use_complete_commit_shas(self) -> None:
        for name in ("check.yml", "discover-upstream.yml", "build-release.yml"):
            text = (WORKFLOWS / name).read_text(encoding="utf-8")
            uses = [line for line in text.splitlines() if line.strip().startswith("uses:")]
            self.assertTrue(uses, name)
            self.assertEqual(len(uses), len(PINNED_USE.findall(text)), name)
            self.assertNotRegex(text, r"@(main|master|latest|v[0-9]+)(?:\s|$)")

    def test_discovery_commits_locks_then_dispatches_every_pending_tag(self) -> None:
        text = (WORKFLOWS / "discover-upstream.yml").read_text(encoding="utf-8")
        self.assertIn("schedule:", text)
        self.assertIn("workflow_dispatch:", text)
        self.assertIn("scripts/resolve_toolchain.py", text)
        self.assertIn("git push origin HEAD:main", text)
        self.assertIn("gh workflow run build-release.yml", text)
        self.assertIn("while IFS=", text)
        self.assertIn("actions/cache/restore@", text)
        self.assertIn("actions/cache/save@", text)

    def test_release_builds_once_and_separates_publish_permissions(self) -> None:
        text = (WORKFLOWS / "build-release.yml").read_text(encoding="utf-8")
        self.assertEqual(1, text.count("scripts/build_libbox.py"))
        self.assertIn("scripts/normalize_aar.py", text)
        self.assertIn("scripts/verify_artifacts.py", text)
        self.assertIn("scripts/create_manifest.py", text)
        self.assertIn("contents: read", text)
        self.assertIn("contents: write", text)
        self.assertIn("id-token: write", text)
        self.assertIn("attestations: write", text)
        self.assertIn("libbox.aar", text)
        self.assertIn("libbox-sources.jar", text)
        self.assertIn("build-manifest.json", text)
        self.assertIn("SHA256SUMS", text)


if __name__ == "__main__":
    unittest.main()
