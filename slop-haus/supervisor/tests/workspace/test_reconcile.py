from __future__ import annotations

import logging
import subprocess
from pathlib import Path

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.workspace.handover import GitSlotRepo
from slop_haus_supervisor.workspace.reconcile import (
    Clone,
    DeferredBusy,
    FetchBase,
    InvalidPath,
    KeepUnpushed,
    Manifest,
    Move,
    MoveConflict,
    ReconcileSettings,
    Remove,
    RepoSpec,
    Report,
    SetRemote,
    WorkspaceReconciler,
    WouldRemove,
    check_path,
)
from support import TempDirTestCase


def git(directory: Path, *args: str) -> str:
    done = subprocess.run(
        ["git", "-C", str(directory), "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", *args],
        capture_output=True, text=True, check=True,
    )  # fmt: skip
    return done.stdout


class FixedSource:
    def __init__(self, manifest: Manifest | None) -> None:
        self.manifest = manifest
        self.on_second_fetch: Manifest | None = None
        self.fetches = 0

    def fetch(self) -> Manifest | None:
        self.fetches += 1
        if self.fetches > 1 and self.on_second_fetch is not None:
            return self.on_second_fetch
        return self.manifest


class ReconcileTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.ws = self.tmp / "slot"
        self.ws.mkdir()
        self.busy = False
        self.settings = ReconcileSettings(destructive=True, removal_confirmed=True)
        self.source = FixedSource(None)

    def remote(self, name: str) -> str:
        """A bare repo at .../github.com/acme/<name>.git with one commit on main; returns the manifest-style remote."""
        bare = self.tmp / "hosts" / "github.com" / "acme" / f"{name}.git"
        bare.mkdir(parents=True)
        git(bare, "init", "--bare", "-b", "main")
        seed = self.tmp / f"seed-{name}"
        subprocess.run(["git", "clone", "-q", str(bare), str(seed)], check=True, capture_output=True)
        (seed / "f.txt").write_text("x\n")
        git(seed, "add", ".")
        git(seed, "commit", "-m", "init")
        git(seed, "push", "origin", "HEAD:main")
        return f"github.com/acme/{name}"

    def url(self, remote: str) -> str:
        return str(self.tmp / "hosts" / f"{remote}.git")

    def reconciler(self) -> WorkspaceReconciler:
        return WorkspaceReconciler(
            self.source,
            SubprocessRunner(),
            GitSlotRepo(SubprocessRunner()),
            logging.getLogger("test"),
            settings=lambda: self.settings,
            busy=lambda _: self.busy,
            remote_url=lambda _existing, remote: self.url(remote),
        )

    def clone(self, spec: RepoSpec) -> Path:
        target = self.ws / spec.path
        target.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["git", "clone", "-q", self.url(spec.remote), str(target)], check=True, capture_output=True)
        return target

    def run_with(self, revision: str, *repos: RepoSpec) -> Report:
        self.source.manifest = Manifest(revision, tuple(repos))
        report = self.reconciler().reconcile(self.ws)
        assert report is not None
        return report

    def seeded(self, *repos: RepoSpec) -> None:
        """The workspace holds `repos` and has recorded them, as a first reconcile does."""
        for spec in repos:
            self.clone(spec)
        self.run_with("r1", *repos)


class PlanTest(ReconcileTest):
    def test_a_missing_repo_is_planned_as_a_clone_and_left_to_update_sh(self) -> None:
        app = RepoSpec("app", self.remote("app"))
        report = self.run_with("r1", app)
        self.assertEqual([Clone("app", app.remote)], report.actions)
        self.assertFalse((self.ws / "app").exists())

    def test_paths_that_could_escape_are_refused_and_nothing_is_joined(self) -> None:
        for bad in ("/etc", "../x", "a/../../x", ".slop-haus/x", ".git", "", "a//b", "a/./b", "a/b/c/d", "a\\b"):
            self.assertIsNotNone(check_path(bad), bad)
        for good in ("app", "apps/web", "a/b/c"):
            self.assertIsNone(check_path(good), good)

    def test_invalid_nested_symlinked_and_malformed_entries_are_reported_and_skipped(self) -> None:
        outside = self.tmp / "outside"
        outside.mkdir()
        (self.ws / "link").symlink_to(outside)
        report = self.run_with(
            "r1", RepoSpec("../x", "github.com/acme/a"), RepoSpec("app", "github.com/acme/app"), RepoSpec("app/sub", "github.com/acme/s"),
            RepoSpec("link/x", "github.com/acme/l"), RepoSpec("ok", "-bad remote"),
        )  # fmt: skip
        invalid = [a.path for a in report.actions if isinstance(a, InvalidPath)]
        self.assertEqual(["../x", "app/sub", "link/x", "ok"], invalid)
        self.assertEqual([Clone("app", "github.com/acme/app")], [a for a in report.actions if isinstance(a, Clone)])
        self.assertEqual([], list(outside.iterdir()))

    def test_no_manifest_leaves_the_workspace_alone(self) -> None:
        self.assertIsNone(self.reconciler().reconcile(self.ws))

    def test_a_remote_change_re_points_origin_and_fetches(self) -> None:
        old, new = self.remote("old"), self.remote("new")
        self.seeded(RepoSpec("app", old))
        report = self.run_with("r2", RepoSpec("app", new))
        self.assertEqual([SetRemote("app", new)], report.actions)
        self.assertEqual([], report.failed)
        git(self.ws / "app", "rev-parse", "--verify", "refs/remotes/origin/main")
        self.assertEqual(self.url(new), git(self.ws / "app", "config", "--get", "remote.origin.url").strip())

    def test_a_changed_base_is_fetched(self) -> None:
        remote = self.remote("app")
        self.seeded(RepoSpec("app", remote, "main"))
        git(self.tmp / "seed-app", "push", "origin", "HEAD:release")
        report = self.run_with("r2", RepoSpec("app", remote, "release"))
        self.assertEqual([FetchBase("app", "release")], report.actions)
        self.assertEqual([], report.failed)
        git(self.ws / "app", "rev-parse", "--verify", "refs/remotes/origin/release")

    def test_a_failed_fetch_is_reported_and_retried_not_recorded(self) -> None:
        remote = self.remote("app")
        self.seeded(RepoSpec("app", remote, "main"))
        report = self.run_with("r2", RepoSpec("app", remote, "no-such-branch"))
        self.assertEqual(["FetchBase app"], report.failed)
        self.assertEqual([FetchBase("app", "no-such-branch")], self.run_with("r2", RepoSpec("app", remote, "no-such-branch")).actions)

    def test_a_checkout_with_no_origin_gets_one(self) -> None:
        remote = self.remote("app")
        self.seeded(RepoSpec("app", remote))
        git(self.ws / "app", "remote", "remove", "origin")
        report = self.run_with("r2", RepoSpec("app", remote))
        self.assertEqual([SetRemote("app", remote)], report.actions)
        self.assertEqual(self.url(remote), git(self.ws / "app", "config", "--get", "remote.origin.url").strip())


class RemoveTest(ReconcileTest):
    def setUp(self) -> None:
        super().setUp()
        self.keep = RepoSpec("keep", self.remote("keep"))
        self.gone = RepoSpec("gone", self.remote("gone"))
        self.seeded(self.keep, self.gone)

    def test_a_clean_removed_repo_is_deleted(self) -> None:
        report = self.run_with("r2", self.keep)
        self.assertEqual([Remove("gone")], report.actions)
        self.assertFalse((self.ws / "gone").exists())
        self.assertTrue((self.ws / "keep").exists())

    def test_removal_is_off_by_default(self) -> None:
        self.settings = ReconcileSettings()
        report = self.run_with("r2", self.keep)
        self.assertEqual([], report.actions)
        self.assertTrue((self.ws / "gone").exists())

    def test_the_first_pass_is_a_dry_run_until_the_owner_confirms(self) -> None:
        self.settings = ReconcileSettings(destructive=True, removal_confirmed=False)
        report = self.run_with("r2", self.keep)
        self.assertEqual([WouldRemove("gone")], report.actions)
        self.assertTrue((self.ws / "gone").exists())
        self.settings = ReconcileSettings(destructive=True, removal_confirmed=True)
        self.run_with("r2", self.keep)
        self.assertFalse((self.ws / "gone").exists())

    def assert_kept(self, reason: str) -> None:
        report = self.run_with("r2", self.keep)
        self.assertEqual([KeepUnpushed("gone", "remove", reason)], report.actions)
        self.assertTrue((self.ws / "gone").exists())
        # Judged again on the next pass, not forgotten.
        self.assertEqual([KeepUnpushed("gone", "remove", reason)], self.run_with("r2", self.keep).actions)

    def test_uncommitted_changes_keep_it(self) -> None:
        (self.ws / "gone" / "f.txt").write_text("edited\n")
        self.assert_kept("uncommitted changes")

    def test_untracked_files_keep_it(self) -> None:
        (self.ws / "gone" / "new.txt").write_text("n\n")
        self.assert_kept("untracked files")

    def test_a_stash_keeps_it(self) -> None:
        (self.ws / "gone" / "f.txt").write_text("edited\n")
        git(self.ws / "gone", "stash")
        self.assert_kept("stashed changes")

    def test_unpushed_commits_keep_it(self) -> None:
        (self.ws / "gone" / "f.txt").write_text("edited\n")
        git(self.ws / "gone", "commit", "-am", "local")
        self.assert_kept("commits no remote has")

    def test_an_invalid_manifest_entry_deletes_nothing_and_forgets_nothing(self) -> None:
        report = self.run_with("r2", self.keep, RepoSpec("../x", "github.com/acme/x"))
        self.assertTrue(any(isinstance(a, InvalidPath) for a in report.actions))
        self.assertFalse(any(isinstance(a, (Remove, WouldRemove)) for a in report.actions))
        self.assertTrue((self.ws / "gone").exists())
        self.assertEqual([Remove("gone")], self.run_with("r3", self.keep).actions)

    def test_a_clone_the_workspace_never_recorded_is_not_touched(self) -> None:
        self.clone(RepoSpec("scratch", self.remote("scratch")))
        self.run_with("r2", self.keep)
        self.assertTrue((self.ws / "scratch").exists())

    def test_a_replaced_checkout_is_not_deleted(self) -> None:
        git(self.ws / "gone", "remote", "set-url", "origin", self.url(self.keep.remote))
        report = self.run_with("r2", self.keep)
        self.assertIsInstance(report.actions[0], KeepUnpushed)
        self.assertTrue((self.ws / "gone").exists())

    def test_a_process_in_the_workspace_defers_it(self) -> None:
        self.busy = True
        report = self.run_with("r2", self.keep)
        self.assertEqual([DeferredBusy("a process is running in the workspace")], [a for a in report.actions if isinstance(a, DeferredBusy)])
        self.assertTrue((self.ws / "gone").exists())

    def test_a_held_lock_defers_it(self) -> None:
        import fcntl

        lock = self.ws / ".slop-haus" / "sync.lock"
        with lock.open("a") as held:
            fcntl.flock(held, fcntl.LOCK_EX)
            report = self.run_with("r2", self.keep)
        self.assertEqual([DeferredBusy("the workspace is locked")], [a for a in report.actions if isinstance(a, DeferredBusy)])
        self.assertTrue((self.ws / "gone").exists())

    def test_nothing_is_applied_when_the_manifest_moved_since_the_plan(self) -> None:
        self.source.manifest = Manifest("r2", (self.keep,))
        self.source.fetches = 0
        self.source.on_second_fetch = Manifest("r3", (self.keep, self.gone))
        report = self.reconciler().reconcile(self.ws)
        assert report is not None
        self.assertTrue(report.stale)
        self.assertTrue((self.ws / "gone").exists())


class MoveTest(ReconcileTest):
    def setUp(self) -> None:
        super().setUp()
        self.a = RepoSpec("a", self.remote("a"))
        self.b = RepoSpec("b", self.remote("b"))
        self.seeded(self.a, self.b)

    def test_a_clean_repo_is_moved_not_recloned(self) -> None:
        marker = git(self.ws / "a", "rev-parse", "HEAD")
        report = self.run_with("r2", RepoSpec("apps/a", self.a.remote), self.b)
        self.assertEqual([Move("a", "apps/a")], report.actions)
        self.assertFalse((self.ws / "a").exists())
        self.assertEqual(marker, git(self.ws / "apps" / "a", "rev-parse", "HEAD"))
        self.assertEqual([], self.run_with("r2", RepoSpec("apps/a", self.a.remote), self.b).actions)

    def test_a_repo_with_work_stays_where_it_is(self) -> None:
        (self.ws / "a" / "new.txt").write_text("n\n")
        report = self.run_with("r2", RepoSpec("apps/a", self.a.remote), self.b)
        self.assertEqual([KeepUnpushed("a", "move", "untracked files")], [a for a in report.actions if isinstance(a, KeepUnpushed)])
        self.assertTrue((self.ws / "a").exists())
        self.assertFalse((self.ws / "apps" / "a").exists())

    def test_a_swap_is_refused_whole(self) -> None:
        report = self.run_with("r2", RepoSpec("a", self.b.remote), RepoSpec("b", self.a.remote))
        self.assertTrue(any(isinstance(a, MoveConflict) for a in report.actions))
        self.assertFalse(any(isinstance(a, (Move, SetRemote)) for a in report.actions))
        self.assertTrue(git(self.ws / "a", "config", "--get", "remote.origin.url").strip().endswith("acme/a.git"))

    def test_a_target_that_exists_is_a_conflict(self) -> None:
        (self.ws / "apps" / "a").mkdir(parents=True)
        report = self.run_with("r2", RepoSpec("apps/a", self.a.remote), self.b)
        self.assertEqual([MoveConflict(("a", "apps/a"), "the target is taken, or the paths swap or form a cycle")], [a for a in report.actions if not isinstance(a, Clone)])
        self.assertTrue((self.ws / "a").exists())

    def test_one_conflict_holds_back_the_other_moves_too(self) -> None:
        (self.ws / "apps" / "b").mkdir(parents=True)
        report = self.run_with("r2", RepoSpec("apps/a", self.a.remote), RepoSpec("apps/b", self.b.remote))
        self.assertFalse(any(isinstance(a, Move) for a in report.actions))
        self.assertTrue((self.ws / "a").exists())
        self.assertTrue((self.ws / "b").exists())
