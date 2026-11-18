import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.workspace.run_dir import run_directory


class RunDirectoryTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)

    def repo(self, name: str) -> Path:
        path = self.root / name
        (path / ".git").mkdir(parents=True)
        return path

    def test_a_ticket_runs_in_the_repo_its_paths_start_with(self) -> None:
        app = self.repo("app")
        self.repo("deploy")
        self.assertEqual(run_directory(self.root, ("app/src/main.go", "app/docs")), app)

    def test_a_file_path_still_finds_its_repo(self) -> None:
        app = self.repo("app")
        (app / "README.md").write_text("x")
        self.assertEqual(run_directory(self.root, ("app/README.md",)), app)

    def test_a_workspace_whose_root_is_the_repo_runs_at_the_root(self) -> None:
        (self.root / ".git").mkdir()
        (self.root / "docs").mkdir()
        self.assertEqual(run_directory(self.root, ("docs/a.md",)), self.root)

    def test_no_paths_run_at_the_root(self) -> None:
        self.repo("app")
        self.assertEqual(run_directory(self.root, ()), self.root)

    def test_a_path_that_escapes_the_root_is_ignored(self) -> None:
        self.assertEqual(run_directory(self.root, ("../elsewhere",)), self.root)
        self.assertEqual(run_directory(self.root, ("/etc",)), self.root)

    def test_a_symlinked_repo_is_not_followed(self) -> None:
        elsewhere = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: __import__("shutil").rmtree(elsewhere, ignore_errors=True))
        (elsewhere / ".git").mkdir()
        (self.root / "linked").symlink_to(elsewhere)
        self.assertEqual(run_directory(self.root, ("linked/x",)), self.root)


if __name__ == "__main__":
    unittest.main()
