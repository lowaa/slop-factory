from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.fleet.wiring import _repo_roots  # pyright: ignore[reportPrivateUsage]
from slop_haus_supervisor.remote_control.repo.workspace_copy import WorkspaceCopy
from slop_haus_supervisor.workspace.layout_report import describe_layout
from slop_haus_supervisor.workspace.repos import find_clones


def git_init(directory: Path) -> Path:
    directory.mkdir(parents=True)
    subprocess.run(["git", "init", "-q", str(directory)], check=True)
    return directory


class FindClonesTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)

    def names(self, depth: int) -> list[str]:
        return [path.relative_to(self.root).as_posix() for path in find_clones(self.root, depth).paths]

    def test_clones_are_found_down_to_the_depth_given(self) -> None:
        git_init(self.root / "one")
        git_init(self.root / "group" / "two")
        git_init(self.root / "a" / "b" / "three")

        self.assertEqual(["one"], self.names(1))
        self.assertEqual(["group/two", "one"], self.names(2))
        self.assertEqual(["a/b/three", "group/two", "one"], self.names(3))

    def test_a_clone_is_not_searched_for_more(self) -> None:
        git_init(self.root / "outer")
        git_init(self.root / "outer" / "vendor" / "inner")

        self.assertEqual(["outer"], self.names(3))

    def test_hidden_folders_are_not_looked_in(self) -> None:
        git_init(self.root / ".slop-haus" / "hidden")

        self.assertEqual([], self.names(3))
        self.assertTrue(find_clones(self.root).trusted)

    def test_a_symlink_is_not_followed_and_makes_the_answer_untrusted(self) -> None:
        elsewhere = git_init(self.root / "elsewhere")
        nested = self.root / "slot"
        nested.mkdir()
        (nested / "link").symlink_to(elsewhere, target_is_directory=True)

        found = find_clones(nested)

        self.assertEqual([], found.paths)
        self.assertFalse(found.trusted)

    def test_a_symlinked_dot_git_is_not_taken_for_a_clone(self) -> None:
        real = git_init(self.root / "real")
        fake = self.root / "slot" / "fake"
        fake.mkdir(parents=True)
        (fake / ".git").symlink_to(real / ".git", target_is_directory=True)

        found = find_clones(self.root / "slot")

        self.assertEqual([], found.paths)
        self.assertFalse(found.trusted)

    def test_a_missing_root_is_untrusted(self) -> None:
        self.assertFalse(find_clones(self.root / "missing").trusted)


class EveryScannerAgreesTest(unittest.TestCase):
    """The callers once looked 1, 2 and 3 folders deep: a clone at depth 3 was seen by some and not others."""

    def test_a_clone_three_folders_down_is_seen_by_every_caller(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for rel in ("top", "group/mid", "a/b/deep"):
                git_init(root / rel)
            expected = ["a/b/deep", "group/mid", "top"]

            self.assertEqual(expected, [p.relative_to(root).as_posix() for p in find_clones(root).paths])
            self.assertEqual(expected, [p.relative_to(root).as_posix() for p in _repo_roots(root)])
            self.assertEqual(expected, list(WorkspaceCopy(str(root)).repos()))
            layout = describe_layout(root)
            assert layout is not None
            self.assertEqual(expected, [repo["path"] for repo in layout["repos"]])


if __name__ == "__main__":
    unittest.main()
