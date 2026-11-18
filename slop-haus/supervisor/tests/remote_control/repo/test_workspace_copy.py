from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

from slop_haus_supervisor.remote_control.repo.workspace_copy import WorkspaceCopy


def git(repo: str, *args: str) -> str:
    out = subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True)
    return out.stdout.strip()


class NestedWorkspaceCase(unittest.TestCase):
    """A plain directory with two checkouts below it, as a nested workspace is."""

    def setUp(self) -> None:
        self.root = tempfile.mkdtemp(prefix="remote-control-nested-test-")
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        env = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        patcher = mock.patch.dict(os.environ, env)
        patcher.start()
        self.addCleanup(patcher.stop)
        for name in ("alpha", "beta", "group/gamma"):
            self.make_repo(name)
        self.copy = WorkspaceCopy(self.root)

    def make_repo(self, name: str) -> None:
        path = os.path.join(self.root, name)
        os.makedirs(path)
        git(path, "init", "-q", "-b", "main")
        self.write(name, "a.txt", f"one {name}")
        git(path, "add", "a.txt")
        git(path, "commit", "-q", "-m", "first")

    def write(self, name: str, file: str, body: str) -> None:
        with open(os.path.join(self.root, name, file), "w") as f:
            f.write(body)

    def test_the_repos_are_the_checkouts_below_a_root_that_is_not_one(self) -> None:
        os.makedirs(os.path.join(self.root, "docs"))
        os.makedirs(os.path.join(self.root, ".hidden", "x", ".git"))

        self.assertEqual(["alpha", "beta", "group/gamma"], sorted(self.copy.repos()))

    def test_a_root_that_is_a_repo_is_just_that_repo(self) -> None:
        git(self.root, "init", "-q", "-b", "main")

        self.assertEqual([""], list(self.copy.repos()))

    def test_the_base_is_each_repos_head(self) -> None:
        base = self.copy.base_commit()

        self.assertEqual(
            {rel: git(os.path.join(self.root, rel), "rev-parse", "HEAD") for rel in ("alpha", "beta", "group/gamma")},
            json.loads(base),
        )

    def test_work_in_one_repo_is_reported_with_paths_from_the_root(self) -> None:
        base = self.copy.base_commit()
        before = self.copy.snapshot()
        self.write("beta", "b.txt", "new")

        state = self.copy.repo_state(base, before, [])

        self.assertEqual(["beta/b.txt"], state["changed_paths"])
        self.assertEqual(1, state["changed_total"])
        self.assertTrue(state["dirty"])
        self.assertEqual(1, state["dirty_count"])
        self.assertEqual("main", state["branch"])
        self.assertEqual(git(os.path.join(self.root, "beta"), "rev-parse", "HEAD"), state["head"])

    def test_commits_of_both_repos_are_counted(self) -> None:
        base = self.copy.base_commit()
        before = self.copy.snapshot()
        for name in ("alpha", "beta"):
            self.write(name, "c.txt", "x")
            git(os.path.join(self.root, name), "add", "c.txt")
            git(os.path.join(self.root, name), "commit", "-q", "-m", f"change {name}")

        state = self.copy.repo_state(base, before, [])

        self.assertEqual(["alpha/c.txt", "beta/c.txt"], state["changed_paths"])
        self.assertEqual(["change alpha", "change beta"], sorted(c["subject"] for c in state["commits"]))
        self.assertEqual(5, state["ahead"], "no upstream: every commit no remote holds, in all three repos")

    def test_work_in_a_repo_two_folders_down_is_reported_from_the_root(self) -> None:
        base = self.copy.base_commit()
        self.write("group/gamma", "g.txt", "new")

        state = self.copy.repo_state(base, None, [])

        self.assertEqual(["group/gamma/g.txt"], state["changed_paths"])

    def test_a_repo_cloned_after_the_base_was_taken_has_no_changes_counted_yet(self) -> None:
        base = self.copy.base_commit()
        self.make_repo("gamma")

        state = self.copy.repo_state(base, None, [])

        self.assertEqual([], state["changed_paths"])
