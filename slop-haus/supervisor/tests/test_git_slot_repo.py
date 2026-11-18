from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.workspace.handover import SNAPSHOT_MESSAGE, GitSlotRepo

BRANCH = "ticket-1-a1"


def git(cwd: Path, *args: str) -> str:
    env = ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"]
    return subprocess.run(["git", *env, *args], cwd=cwd, check=True, capture_output=True, text=True).stdout


class GitSlotRepoTest(unittest.TestCase):
    """Against real git and a local bare origin: the commands are the behaviour."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.origin = root / "origin.git"
        self.work = root / "work"
        git(root, "init", "--bare", "-b", "main", str(self.origin))
        git(root, "clone", str(self.origin), str(self.work))
        (self.work / "tracked.txt").write_text("one\n")
        git(self.work, "add", "tracked.txt")
        git(self.work, "commit", "-m", "init")
        git(self.work, "push", "origin", "HEAD:main")
        git(self.work, "fetch", "origin")
        # slop-haus creates the attempt's branch on the server before the slot sees it.
        git(self.work, "switch", "-c", BRANCH)
        git(self.work, "push", "origin", BRANCH)
        git(self.work, "fetch", "origin")
        self.repo = GitSlotRepo(SubprocessRunner())

    def test_clean_checkout_has_no_unpushed_work(self) -> None:
        self.assertFalse(self.repo.has_unpushed_work(self.work, BRANCH))

    def _nested_slot(self) -> Path:
        slot = self.work.parent / "slot"
        slot.mkdir()
        git(slot, "clone", str(self.origin), str(slot / "app"))
        return slot

    def test_nested_slot_with_pushed_clones_has_nothing_unpushed(self) -> None:
        self.assertFalse(self.repo.has_unpushed_anywhere(self._nested_slot()))

    def test_nested_slot_with_an_unpushed_commit_in_a_clone_counts(self) -> None:
        slot = self._nested_slot()
        (slot / "app" / "new.txt").write_text("x\n")
        git(slot / "app", "add", "new.txt")
        git(slot / "app", "commit", "-m", "local")
        self.assertTrue(self.repo.has_unpushed_anywhere(slot))

    def test_nested_slot_checks_every_clone(self) -> None:
        slot = self._nested_slot()
        git(slot, "clone", str(self.origin), str(slot / "group" / "second"))
        (slot / "group" / "second" / "tracked.txt").write_text("edited\n")
        self.assertTrue(self.repo.has_unpushed_anywhere(slot))

    def test_nested_slot_with_a_tracked_edit_counts(self) -> None:
        slot = self._nested_slot()
        (slot / "app" / "tracked.txt").write_text("edited\n")
        self.assertTrue(self.repo.has_unpushed_anywhere(slot))

    def test_nested_slot_with_a_symlinked_entry_counts_as_unpushed(self) -> None:
        slot = self._nested_slot()
        (slot / "link").symlink_to(self.work, target_is_directory=True)
        self.assertTrue(self.repo.has_unpushed_anywhere(slot))

    def test_nested_slot_with_no_clone_counts_as_unpushed(self) -> None:
        empty = self.work.parent / "empty"
        empty.mkdir()
        self.assertTrue(self.repo.has_unpushed_anywhere(empty))

    def test_commit_on_another_branch_does_not_count_as_pushed(self) -> None:
        (self.work / "tracked.txt").write_text("two\n")
        git(self.work, "commit", "-am", "local")
        git(self.work, "push", "origin", "HEAD:main")
        git(self.work, "fetch", "origin")
        self.assertTrue(self.repo.has_unpushed_work(self.work, BRANCH))

    def test_tracked_edit_and_unpushed_commit_both_count(self) -> None:
        (self.work / "tracked.txt").write_text("two\n")
        self.assertTrue(self.repo.has_unpushed_work(self.work, BRANCH))
        git(self.work, "commit", "-am", "local")
        self.assertTrue(self.repo.has_unpushed_work(self.work, BRANCH))

    def test_safety_push_commits_tracked_only_and_never_untracked(self) -> None:
        (self.work / "tracked.txt").write_text("two\n")
        (self.work / "secret.env").write_text("KEY=1\n")
        result = self.repo.safety_push(self.work, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(result.untracked, ["secret.env"])
        self.assertEqual(git(self.origin, "log", "-1", "--format=%s", BRANCH).strip(), SNAPSHOT_MESSAGE)
        self.assertNotIn("secret.env", git(self.origin, "ls-tree", "-r", "--name-only", BRANCH))
        self.assertEqual(git(self.origin, "rev-parse", BRANCH).strip(), result.commit)
        self.assertFalse(self.repo.has_unpushed_work(self.work, BRANCH))

    def _supersede(self) -> str:
        """The superseded shape: local = change + merge of main; origin = the same change cherry-picked onto base."""
        base = git(self.work, "rev-parse", "HEAD").strip()
        (self.work / "change.txt").write_text("change\n")
        git(self.work, "add", "change.txt")
        git(self.work, "commit", "-m", "change")
        change = git(self.work, "rev-parse", "HEAD").strip()
        git(self.work, "switch", "-c", "main-work", "main")
        (self.work / "other.txt").write_text("main moved\n")
        git(self.work, "add", "other.txt")
        git(self.work, "commit", "-m", "main moves")
        git(self.work, "push", "origin", "HEAD:main")
        git(self.work, "switch", BRANCH)
        git(self.work, "merge", "--no-edit", "main-work")
        git(self.work, "switch", "-c", f"{BRANCH}-fix", base)
        git(self.work, "cherry-pick", change)
        # A cherry-pick within the same second is the very same commit; a real one differs.
        git(self.work, "commit", "--amend", "--no-edit", "--date=2001-01-01T00:00:00")
        git(self.work, "push", "--force", "origin", f"HEAD:{BRANCH}")
        git(self.work, "fetch", "origin")
        git(self.work, "switch", BRANCH)
        return git(self.work, "rev-parse", f"origin/{BRANCH}").strip()

    def test_branch_superseded_by_a_rewritten_remote_is_not_unpushed(self) -> None:
        self._supersede()
        self.assertFalse(self.repo.has_unpushed_work(self.work, BRANCH))

    def test_superseded_branch_is_reset_and_its_helper_deleted(self) -> None:
        remote = self._supersede()
        git(self.work, "switch", "main")
        self.assertTrue(self.repo.drop_superseded(self.work, BRANCH))
        self.assertEqual(remote, git(self.work, "rev-parse", BRANCH).strip())
        self.assertEqual("", git(self.work, "branch", "--list", f"{BRANCH}-fix").strip())

    def test_diverged_branch_with_a_real_patch_is_not_superseded(self) -> None:
        self._supersede()
        (self.work / "extra.txt").write_text("mine\n")
        git(self.work, "add", "extra.txt")
        git(self.work, "commit", "-m", "extra")
        self.assertTrue(self.repo.has_unpushed_work(self.work, BRANCH))
        self.assertFalse(self.repo.drop_superseded(self.work, BRANCH))

    def test_safety_push_of_a_diverged_branch_goes_to_a_rescue_ref(self) -> None:
        self._supersede()
        (self.work / "extra.txt").write_text("mine\n")
        git(self.work, "add", "extra.txt")
        git(self.work, "commit", "-m", "extra")
        mine = git(self.work, "rev-parse", "HEAD").strip()
        theirs = git(self.origin, "rev-parse", BRANCH).strip()
        result = self.repo.safety_push(self.work, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertTrue(result.rescue_ref.startswith(f"refs/heads/{BRANCH}-rescue-"))
        self.assertEqual(mine, git(self.origin, "rev-parse", result.rescue_ref).strip())
        self.assertEqual(theirs, git(self.origin, "rev-parse", BRANCH).strip())

    def test_safety_push_fails_when_the_rescue_push_fails_too(self) -> None:
        git(self.work, "remote", "set-url", "origin", str(self.origin.parent / "gone.git"))
        (self.work / "tracked.txt").write_text("two\n")
        git(self.work, "commit", "-am", "local")
        result = self.repo.safety_push(self.work, BRANCH)
        self.assertFalse(result.ok)
        self.assertEqual("", result.rescue_ref)

    def test_ahead_only_branch_still_pushes_to_its_own_name(self) -> None:
        (self.work / "tracked.txt").write_text("two\n")
        git(self.work, "commit", "-am", "local")
        result = self.repo.safety_push(self.work, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertEqual("", result.rescue_ref)
        self.assertEqual(git(self.work, "rev-parse", "HEAD").strip(), git(self.origin, "rev-parse", BRANCH).strip())

    def test_safety_push_is_never_forced(self) -> None:
        # Someone else advanced the branch; our diverging commit must not overwrite it.
        (self.work / "tracked.txt").write_text("theirs\n")
        git(self.work, "commit", "-am", "theirs")
        git(self.work, "push", "origin", f"HEAD:{BRANCH}")
        git(self.work, "reset", "--hard", "HEAD~1")
        (self.work / "tracked.txt").write_text("ours\n")
        result = self.repo.safety_push(self.work, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertNotEqual("", result.rescue_ref)
        self.assertEqual("theirs", git(self.origin, "log", "-1", "--format=%s", BRANCH).strip())

    def test_dirty_checkout_on_the_claimed_branch_is_its_own_interrupted_work(self) -> None:
        (self.work / "tracked.txt").write_text("half done\n")
        result = self.repo.prepare_branch(self.work, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertTrue(result.interrupted)
        self.assertEqual("half done\n", (self.work / "tracked.txt").read_text())

    def test_dirty_checkout_on_another_branch_is_still_refused(self) -> None:
        git(self.work, "switch", "-c", "ticket-2-a1")
        (self.work / "tracked.txt").write_text("someone else's\n")
        result = self.repo.prepare_branch(self.work, BRANCH)
        self.assertFalse(result.ok)
        self.assertEqual("someone else's\n", (self.work / "tracked.txt").read_text())

    def test_clean_checkout_is_not_marked_interrupted(self) -> None:
        self.assertFalse(self.repo.prepare_branch(self.work, BRANCH).interrupted)

    def test_interrupted_push_commits_new_files_too_and_the_next_start_sees_it(self) -> None:
        (self.work / "tracked.txt").write_text("two\n")
        (self.work / "new.txt").write_text("fresh\n")
        (self.work / ".gitignore").write_text("ignored.log\n")
        (self.work / "ignored.log").write_text("noise\n")
        result = self.repo.interrupted_push(self.work, 1, BRANCH)
        self.assertTrue(result.ok, result.error)
        files = git(self.origin, "ls-tree", "-r", "--name-only", BRANCH)
        self.assertIn("new.txt", files)
        self.assertNotIn("ignored.log", files)
        subject = git(self.origin, "log", "-1", "--format=%s", BRANCH).strip()
        self.assertIn("interrupted by a supervisor stop", subject)
        self.assertIn("#1", subject)
        self.assertTrue(self.repo.prepare_branch(self.work, BRANCH).interrupted)

    def test_interrupted_push_saves_a_run_whose_only_work_is_a_new_file(self) -> None:
        (self.work / "new.txt").write_text("fresh\n")
        result = self.repo.interrupted_push(self.work, 1, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertIn("new.txt", git(self.origin, "ls-tree", "-r", "--name-only", BRANCH))

    def test_interrupted_push_withholds_secret_looking_and_oversized_files(self) -> None:
        for name in (".env.local", "secrets.env", "k.pem", "id_ed25519", "api_token.txt"):
            (self.work / name).write_text("s\n")
        (self.work / "big.bin").write_bytes(b"0" * (5 * 1024 * 1024 + 1))
        (self.work / "ok.txt").write_text("fine\n")
        result = self.repo.interrupted_push(self.work, 1, BRANCH)
        self.assertTrue(result.ok, result.error)
        self.assertEqual(
            [".env.local", "api_token.txt", "big.bin", "id_ed25519", "k.pem", "secrets.env"], sorted(result.untracked)
        )
        pushed = git(self.origin, "ls-tree", "-r", "--name-only", BRANCH).split()
        self.assertIn("ok.txt", pushed)
        self.assertEqual(["ok.txt", "tracked.txt"], sorted(pushed))
        self.assertTrue((self.work / "secrets.env").exists())

    def test_the_next_start_names_the_withheld_files(self) -> None:
        (self.work / "secrets.env").write_text("s\n")
        (self.work / "ok.txt").write_text("fine\n")
        self.repo.interrupted_push(self.work, 1, BRANCH)
        prepared = self.repo.prepare_branch(self.work, BRANCH)
        self.assertTrue(prepared.interrupted)
        self.assertEqual(["secrets.env"], prepared.withheld)

    def test_interrupted_push_with_nothing_to_save_adds_no_commit(self) -> None:
        before = git(self.origin, "rev-parse", BRANCH)
        self.assertTrue(self.repo.interrupted_push(self.work, 1, BRANCH).ok)
        self.assertEqual(before, git(self.origin, "rev-parse", BRANCH))

    def test_failed_interrupted_push_keeps_the_commit_locally(self) -> None:
        (self.work / "tracked.txt").write_text("theirs\n")
        git(self.work, "commit", "-am", "theirs")
        git(self.work, "push", "origin", f"HEAD:{BRANCH}")
        git(self.work, "reset", "--hard", "HEAD~1")
        (self.work / "tracked.txt").write_text("ours\n")
        result = self.repo.interrupted_push(self.work, 1, BRANCH)
        self.assertFalse(result.ok)
        self.assertIn("interrupted by a supervisor stop", git(self.work, "log", "-1", "--format=%s"))
        self.assertEqual("", git(self.work, "status", "--porcelain", "--untracked-files=no").strip())


if __name__ == "__main__":
    unittest.main()
