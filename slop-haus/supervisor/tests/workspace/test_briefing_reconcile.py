from __future__ import annotations

import logging
import subprocess
import unittest
from pathlib import Path

from slop_haus_supervisor.fleet.wiring import build_workspace_reconciler, reconciler_for
from slop_haus_supervisor.ports import ProcessResult, SubprocessRunner
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.workspace.briefing import BRIEFING, SlotBriefing
from slop_haus_supervisor.workspace.reconcile import STATE_FILE, WorkspaceReconciler
from stub_server import StubServer, serving
from support import TempDirTestCase

MANIFEST = "/api/workspace/manifest/slot"
DESIRED = "/api/supervisor/desired_state"


def git(directory: Path, *args: str) -> str:
    done = subprocess.run(
        ["git", "-C", str(directory), "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", *args],
        capture_output=True, text=True, check=True,
    )  # fmt: skip
    return done.stdout


class NoopUpdater:
    """update.sh stand-in: only the briefing the refresh would leave."""

    def __init__(self, slot: Path) -> None:
        self.slot = slot

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: object = None) -> ProcessResult:
        (self.slot / BRIEFING).write_text("briefing")
        return ProcessResult(0, "", "")


class WiredReconcileTest(TempDirTestCase):
    """A slot with work in a repo the manifest dropped, run through the reconciler the wiring builds."""

    def setUp(self) -> None:
        super().setUp()
        self.slot = self.tmp / "slot"
        (self.slot / BRIEFING).parent.mkdir(parents=True)
        bare = self.tmp / "hosts" / "github.com" / "acme" / "gone.git"
        bare.mkdir(parents=True)
        git(bare, "init", "--bare", "-b", "main")
        seed = self.tmp / "seed"
        subprocess.run(["git", "clone", "-q", str(bare), str(seed)], check=True, capture_output=True)
        (seed / "f.txt").write_text("x\n")
        git(seed, "add", ".")
        git(seed, "commit", "-m", "init")
        git(seed, "push", "origin", "HEAD:main")
        subprocess.run(["git", "clone", "-q", str(bare), str(self.slot / "gone")], check=True, capture_output=True)
        # The recorded remote must still be the origin, or the checkout is held back for that reason instead.
        git(self.slot / "gone", "remote", "set-url", "origin", "https://github.com/acme/gone.git")
        (self.slot / STATE_FILE).parent.mkdir(exist_ok=True)
        (self.slot / STATE_FILE).write_text(
            '[{"path": "gone", "remote": "github.com/acme/gone", "base": ""}]'
        )
        self.stub = StubServer()
        cm = serving(self.stub)
        cm.__enter__()
        self.addCleanup(cm.__exit__, None, None, None)
        self.stub.on("GET", MANIFEST, (200, {"revision": 2, "repos": []}))
        self.reconciler = reconciler_for(SupervisorApi(Transport(self.stub.url), lambda: "t"), SubprocessRunner(), logging.getLogger("test"), removal_confirmed=True)
        self.briefing = SlotBriefing(NoopUpdater(self.slot), logging.getLogger("test"), reconciler=self.reconciler)

    def load(self) -> None:
        self.briefing.load(self.slot)

    def assert_kept(self) -> None:
        self.load()
        self.assertTrue((self.slot / "gone" / "f.txt").exists())
        self.assertEqual(1, len(self.stub.requests("GET", MANIFEST)))

    def test_uncommitted_changes_are_kept(self) -> None:
        (self.slot / "gone" / "f.txt").write_text("edited\n")
        self.assert_kept()
        self.assertEqual("edited\n", (self.slot / "gone" / "f.txt").read_text())

    def test_untracked_files_are_kept(self) -> None:
        (self.slot / "gone" / "new.txt").write_text("n\n")
        self.assert_kept()
        self.assertTrue((self.slot / "gone" / "new.txt").exists())

    def test_a_stash_is_kept(self) -> None:
        (self.slot / "gone" / "f.txt").write_text("edited\n")
        git(self.slot / "gone", "stash")
        self.assert_kept()
        self.assertIn("edited", git(self.slot / "gone", "stash", "show", "-p"))

    def test_unpushed_commits_are_kept(self) -> None:
        (self.slot / "gone" / "f.txt").write_text("edited\n")
        git(self.slot / "gone", "commit", "-am", "local")
        self.assert_kept()
        self.assertEqual("local", git(self.slot / "gone", "log", "-1", "--format=%s").strip())

    def test_a_moved_repo_with_work_stays_where_it_is(self) -> None:
        self.stub.on("GET", MANIFEST, (200, {"revision": 2, "repos": [{"path": "apps/gone", "remote": "github.com/acme/gone", "base": ""}]}))
        (self.slot / "gone" / "new.txt").write_text("n\n")
        self.load()
        self.assertTrue((self.slot / "gone" / "new.txt").exists())
        self.assertFalse((self.slot / "apps" / "gone").exists())

    def test_a_clean_repo_is_removed_once_confirmed_and_not_before(self) -> None:
        api = SupervisorApi(Transport(self.stub.url), lambda: "t")
        dry = SlotBriefing(NoopUpdater(self.slot), logging.getLogger("test"), reconciler=reconciler_for(api, SubprocessRunner(), logging.getLogger("test")))
        dry.load(self.slot)
        self.assertTrue((self.slot / "gone").exists())
        self.load()
        self.assertFalse((self.slot / "gone").exists())

    def test_without_a_manifest_the_workspace_is_left_alone(self) -> None:
        self.stub.on("GET", MANIFEST, (500, {"detail": "down"}))
        (self.slot / "gone" / "new.txt").write_text("n\n")
        self.load()
        self.assertTrue((self.slot / "gone" / "new.txt").exists())


class BuildWorkspaceReconcilerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.stub = StubServer()
        cm = serving(self.stub)
        cm.__enter__()
        self.addCleanup(cm.__exit__, None, None, None)

    def built(self) -> object:
        api = SupervisorApi(Transport(self.stub.url), lambda: "t")
        return build_workspace_reconciler(api, SubprocessRunner(), logging.getLogger("test"))

    def test_it_is_built_only_when_the_flag_is_on(self) -> None:
        self.stub.on("GET", DESIRED, (200, {"agents": [], "workspace_reconcile": True}))
        self.assertIsInstance(self.built(), WorkspaceReconciler)
        self.stub.on("GET", DESIRED, (200, {"agents": [], "workspace_reconcile": False}))
        self.assertIsNone(self.built())
        self.stub.on("GET", DESIRED, (200, {"agents": []}))
        self.assertIsNone(self.built())

    def test_an_unreadable_flag_reads_as_off(self) -> None:
        self.stub.on("GET", DESIRED, (500, {"detail": "down"}))
        self.assertIsNone(self.built())
        self.stub.on("GET", DESIRED, (200, {}))
        self.assertIsNone(self.built())
