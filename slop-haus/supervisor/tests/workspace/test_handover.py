from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.workspace.handover import SNAPSHOT_MESSAGE, GitSlotRepo

A = "ticket-837-a1"
B = "ticket-803-a1"


def git(cwd: Path, *args: str) -> str:
    env = ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"]
    return subprocess.run(["git", *env, *args], cwd=cwd, check=True, capture_output=True, text=True).stdout


class WorktreeHandoverTest(unittest.TestCase):
    """The slot sits on branch A; a run worked ticket branch B in a worktree of its own."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.origin = root / "origin.git"
        self.slot = root / "slot"
        self.wt = root / "w803"
        git(root, "init", "--bare", "-b", "main", str(self.origin))
        git(root, "clone", str(self.origin), str(self.slot))
        (self.slot / "tracked.txt").write_text("one\n")
        git(self.slot, "add", "tracked.txt")
        git(self.slot, "commit", "-m", "init")
        git(self.slot, "push", "origin", "HEAD:main")
        git(self.slot, "branch", B)
        git(self.slot, "push", "origin", B)
        git(self.slot, "switch", "-c", A)
        (self.slot / "tracked.txt").write_text("a-work\n")
        git(self.slot, "commit", "-am", "work on 837")
        git(self.slot, "push", "origin", A)
        git(self.slot, "fetch", "origin")
        git(self.slot, "worktree", "add", str(self.wt), B)
        self.repo = GitSlotRepo(SubprocessRunner())

    def commit_in_worktree(self, text: str) -> None:
        (self.wt / "tracked.txt").write_text(text)
        git(self.wt, "commit", "-am", "work on 803")

    def test_slot_head_on_another_pushed_branch_is_clean(self) -> None:
        self.commit_in_worktree("b-work\n")
        git(self.wt, "push", "origin", B)
        git(self.slot, "fetch", "origin")
        self.assertFalse(self.repo.has_unpushed_work(self.slot, B))

    def test_slot_head_commits_never_reach_the_other_branch(self) -> None:
        # A is ahead of main and not an ancestor of B: a HEAD-based push would be rejected.
        before = git(self.origin, "rev-parse", B)
        self.assertFalse(self.repo.has_unpushed_work(self.slot, B))
        self.assertEqual(git(self.origin, "rev-parse", B), before)

    def test_unpushed_commit_in_worktree_is_pushed_to_its_branch_only(self) -> None:
        self.commit_in_worktree("b-work\n")
        self.assertTrue(self.repo.has_unpushed_work(self.slot, B))
        result = self.repo.safety_push(self.slot, B)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(git(self.origin, "rev-parse", B).strip(), git(self.wt, "rev-parse", "HEAD").strip())
        self.assertEqual(git(self.origin, "show", f"{B}:tracked.txt"), "b-work\n")
        self.assertEqual(git(self.origin, "show", f"{A}:tracked.txt"), "a-work\n")

    def test_uncommitted_change_in_worktree_is_committed_there_and_pushed(self) -> None:
        (self.wt / "tracked.txt").write_text("dirty\n")
        self.assertTrue(self.repo.has_unpushed_work(self.slot, B))
        result = self.repo.safety_push(self.slot, B)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(git(self.origin, "log", "-1", "--format=%s", B).strip(), SNAPSHOT_MESSAGE)
        self.assertEqual(git(self.slot, "rev-parse", "--abbrev-ref", "HEAD").strip(), A)

    def test_untracked_files_in_the_worktree_are_reported_never_committed(self) -> None:
        (self.wt / "tracked.txt").write_text("dirty\n")
        (self.wt / "secret.env").write_text("KEY=1\n")
        result = self.repo.safety_push(self.slot, B)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(result.untracked, ["secret.env"])
        self.assertNotIn("secret.env", git(self.origin, "ls-tree", "-r", "--name-only", B))

    def test_branch_checked_out_nowhere_and_pushed_is_clean(self) -> None:
        self.commit_in_worktree("b-work\n")
        git(self.wt, "push", "origin", B)
        git(self.slot, "worktree", "remove", str(self.wt))
        git(self.slot, "fetch", "origin")
        self.assertFalse(self.repo.has_unpushed_work(self.slot, B))

    def test_branch_checked_out_nowhere_with_commits_ahead_is_pushed(self) -> None:
        self.commit_in_worktree("b-work\n")
        git(self.slot, "worktree", "remove", str(self.wt))
        self.assertTrue(self.repo.has_unpushed_work(self.slot, B))
        self.assertTrue(self.repo.safety_push(self.slot, B).ok)
        self.assertEqual(git(self.origin, "show", f"{B}:tracked.txt"), "b-work\n")

    def test_branch_absent_locally_is_clean_and_pushes_nothing(self) -> None:
        git(self.slot, "worktree", "remove", str(self.wt))
        git(self.slot, "branch", "-D", B)
        before = git(self.origin, "rev-parse", B)
        self.assertFalse(self.repo.has_unpushed_work(self.slot, B))
        self.assertTrue(self.repo.safety_push(self.slot, B).ok)
        self.assertEqual(git(self.origin, "rev-parse", B), before)

    def test_worktree_directory_deleted_by_the_run_still_pushes_the_branch(self) -> None:
        self.commit_in_worktree("b-work\n")
        subprocess.run(["rm", "-rf", str(self.wt)], check=True)
        self.assertTrue(self.repo.has_unpushed_work(self.slot, B))
        self.assertTrue(self.repo.safety_push(self.slot, B).ok)
        self.assertEqual(git(self.origin, "show", f"{B}:tracked.txt"), "b-work\n")

    def test_release_removes_a_clean_worktree_and_prunes_stale_ones(self) -> None:
        self.repo.release_worktrees(self.slot, B)
        self.assertFalse(self.wt.exists())
        stale = self.wt.parent / "gone"
        git(self.slot, "worktree", "add", "-b", "other", str(stale))
        subprocess.run(["rm", "-rf", str(stale)], check=True)
        self.repo.release_worktrees(self.slot, B)
        self.assertNotIn("gone", git(self.slot, "worktree", "list"))

    def test_release_keeps_a_worktree_with_changes(self) -> None:
        (self.wt / "tracked.txt").write_text("dirty\n")
        self.repo.release_worktrees(self.slot, B)
        self.assertTrue(self.wt.exists())

    def test_release_never_removes_the_slot_itself(self) -> None:
        self.repo.release_worktrees(self.slot, A)
        self.assertTrue(self.slot.exists())


class PrepareBranchTest(unittest.TestCase):
    """The slot was left on the previous ticket's branch A; ticket B is claimed."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.origin = root / "origin.git"
        self.slot = root / "slot"
        git(root, "init", "--bare", "-b", "main", str(self.origin))
        git(root, "clone", str(self.origin), str(self.slot))
        (self.slot / "tracked.txt").write_text("one\n")
        git(self.slot, "add", "tracked.txt")
        git(self.slot, "commit", "-m", "init")
        git(self.slot, "push", "origin", "HEAD:main")
        git(self.slot, "branch", B)
        git(self.slot, "push", "origin", B)
        git(self.slot, "switch", "-c", A)
        git(self.slot, "push", "origin", A)
        self.repo = GitSlotRepo(SubprocessRunner())

    def head(self) -> str:
        return git(self.slot, "rev-parse", "--abbrev-ref", "HEAD").strip()

    def test_slot_on_another_branch_is_switched_and_tracks_origin(self) -> None:
        git(self.slot, "branch", "-D", B)
        result = self.repo.prepare_branch(self.slot, B)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(self.head(), B)
        self.assertEqual(git(self.slot, "rev-parse", "--abbrev-ref", f"{B}@{{upstream}}").strip(), f"origin/{B}")

    def test_existing_local_branch_behind_origin_is_brought_level(self) -> None:
        other = self.slot.parent / "other"
        git(self.slot.parent, "clone", str(self.origin), str(other))
        git(other, "switch", B)
        (other / "tracked.txt").write_text("remote\n")
        git(other, "commit", "-am", "remote work")
        git(other, "push", "origin", B)
        self.assertTrue(self.repo.prepare_branch(self.slot, B).ok)
        self.assertEqual((self.slot / "tracked.txt").read_text(), "remote\n")

    def test_unpushed_commits_on_the_branch_are_kept(self) -> None:
        git(self.slot, "switch", B)
        (self.slot / "tracked.txt").write_text("local\n")
        git(self.slot, "commit", "-am", "local work")
        git(self.slot, "switch", A)
        self.assertTrue(self.repo.prepare_branch(self.slot, B).ok)
        self.assertEqual((self.slot / "tracked.txt").read_text(), "local\n")

    def test_dirty_tree_is_refused_and_untouched(self) -> None:
        (self.slot / "tracked.txt").write_text("dirty\n")
        result = self.repo.prepare_branch(self.slot, B)
        self.assertFalse(result.ok)
        self.assertEqual(self.head(), A)
        self.assertEqual((self.slot / "tracked.txt").read_text(), "dirty\n")

    def test_branch_only_local_is_switched_to_and_kept(self) -> None:
        git(self.slot, "branch", "ticket-2-a1")
        self.assertTrue(self.repo.prepare_branch(self.slot, "ticket-2-a1").ok)
        self.assertEqual(self.head(), "ticket-2-a1")

    def test_branch_missing_everywhere_is_cut_from_the_base_and_pushed(self) -> None:
        (self.slot / "tracked.txt").write_text("two\n")
        git(self.slot, "commit", "-qam", "second")
        git(self.slot, "push", "origin", f"{A}:main")
        base = git(self.slot, "rev-parse", "HEAD~1").strip()
        prepared = self.repo.prepare_branch(self.slot, "ticket-1-a1", base)
        self.assertTrue(prepared.ok, prepared.error)
        self.assertIn("ticket-1-a1", prepared.warning)
        self.assertEqual(self.head(), "ticket-1-a1")
        self.assertEqual(git(self.origin, "rev-parse", "ticket-1-a1").strip(), base)

    def test_branch_missing_everywhere_without_a_base_is_cut_from_origin_main(self) -> None:
        self.assertTrue(self.repo.prepare_branch(self.slot, "ticket-1-a1").ok)
        self.assertEqual(git(self.origin, "rev-parse", "ticket-1-a1").strip(), git(self.origin, "rev-parse", "main").strip())

    def test_branch_deleted_on_origin_after_an_earlier_attempt_is_cut_afresh(self) -> None:
        git(self.slot, "fetch", "origin")
        git(self.origin, "update-ref", "-d", f"refs/heads/{B}")
        git(self.slot, "branch", "-D", B)
        self.assertEqual(git(self.slot, "rev-parse", "--verify", "-q", f"origin/{B}").strip() != "", True)  # stale ref
        prepared = self.repo.prepare_branch(self.slot, B)
        self.assertTrue(prepared.ok, prepared.error)
        self.assertIn(B, prepared.warning)
        self.assertEqual(git(self.origin, "rev-parse", B).strip(), git(self.origin, "rev-parse", "main").strip())

    def test_a_failed_push_leaves_no_local_branch_to_be_taken_for_ready(self) -> None:
        git(self.slot, "remote", "set-url", "--push", "origin", str(self.slot.parent / "nowhere.git"))
        self.assertFalse(self.repo.prepare_branch(self.slot, "ticket-1-a1").ok)
        self.assertEqual(self.head(), A)
        self.assertEqual(git(self.slot, "branch", "--list", "ticket-1-a1").strip(), "")

    def test_missing_branch_on_a_dirty_tree_is_not_cut(self) -> None:
        (self.slot / "tracked.txt").write_text("dirty\n")
        self.assertFalse(self.repo.prepare_branch(self.slot, "ticket-1-a1").ok)
        self.assertEqual(self.head(), A)

    def test_check_clean_leaves_the_checkout_where_it_is(self) -> None:
        self.assertTrue(self.repo.check_clean(self.slot).ok)
        self.assertEqual(self.head(), A)

    def test_check_clean_refuses_a_dirty_tree(self) -> None:
        (self.slot / "tracked.txt").write_text("dirty\n")
        self.assertFalse(self.repo.check_clean(self.slot).ok)

    def test_commit_on_the_previous_branch_is_seen_as_stray(self) -> None:
        self.assertTrue(self.repo.prepare_branch(self.slot, B).ok)
        before = self.repo.snapshot(self.slot)
        self.assertEqual(self.repo.stray_work(self.slot, B, before), [])
        git(self.slot, "switch", A)
        (self.slot / "tracked.txt").write_text("misplaced\n")
        git(self.slot, "commit", "-am", "misplaced")
        stray = self.repo.stray_work(self.slot, B, before)
        self.assertEqual(len(stray), 2, stray)
        self.assertIn("HEAD is on " + A, stray[0])
        self.assertIn(A, stray[1])
        self.assertEqual(git(self.origin, "rev-parse", A).strip(), git(self.slot, "rev-parse", f"origin/{A}").strip())

    def test_commits_on_the_claimed_branch_are_not_stray(self) -> None:
        self.assertTrue(self.repo.prepare_branch(self.slot, B).ok)
        before = self.repo.snapshot(self.slot)
        (self.slot / "tracked.txt").write_text("mine\n")
        git(self.slot, "commit", "-am", "mine")
        self.assertEqual(self.repo.stray_work(self.slot, B, before), [])


if __name__ == "__main__":
    unittest.main()
