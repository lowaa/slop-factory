from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.workspace.layout_report import describe_layout, strip_userinfo


def git_init(directory: Path, origin: str | None = None) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    subprocess.run(["git", "init", "-q", str(directory)], check=True)
    if origin:
        subprocess.run(["git", "-C", str(directory), "remote", "add", "origin", origin], check=True)


class DescribeLayoutTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)

    def test_a_nested_layout_has_no_root_repo_and_the_child_with_its_origin(self) -> None:
        git_init(self.root / "slop-factory", "https://user:tok@github.com/lowaa/slop-factory.git")
        (self.root / ".slop-haus").mkdir()
        (self.root / "notes").mkdir()

        self.assertEqual(
            {
                "root": {"path": ".", "git": False, "origin": ""},
                "repos": [{"path": "slop-factory", "git": True, "origin": "github.com/lowaa/slop-factory.git"}],
            },
            describe_layout(self.root),
        )

    def test_a_root_clone_is_reported_as_the_root(self) -> None:
        git_init(self.root, "git@github.com:o/r.git")

        layout = describe_layout(self.root)

        assert layout is not None
        self.assertEqual({"path": ".", "git": True, "origin": "github.com:o/r.git"}, layout["root"])

    def test_a_missing_clone_reports_no_repos(self) -> None:
        layout = describe_layout(self.root)

        assert layout is not None
        self.assertEqual([], layout["repos"])

    def test_a_symlinked_repo_is_not_followed(self) -> None:
        with tempfile.TemporaryDirectory() as elsewhere:
            git_init(Path(elsewhere) / "r", "h/x")
            (self.root / "link").symlink_to(Path(elsewhere) / "r")

            layout = describe_layout(self.root)

        assert layout is not None
        self.assertEqual([], layout["repos"])

    def test_an_origin_past_the_wire_limit_means_no_report(self) -> None:
        git_init(self.root / "r", "h/" + "x" * 600)

        self.assertIsNone(describe_layout(self.root))

    def test_an_unreadable_root_means_no_report_not_an_empty_one(self) -> None:
        self.assertIsNone(describe_layout(self.root / "gone"))

    def test_credentials_never_survive_in_a_remote(self) -> None:
        self.assertEqual("h/a/b", strip_userinfo("https://u:p/ss?x@h/a/b"))
        self.assertEqual("h/a", strip_userinfo("ssh://git@h/a"))
