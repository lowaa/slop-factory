from __future__ import annotations

import contextlib
import fcntl
import io
import threading
import time
from collections.abc import Generator

from slop_haus_supervisor import cli
from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.service.control import ControlSpool
from slop_haus_supervisor.workspace.layout import Layout
from support import TempDirTestCase, make_archive


class PausedSlotsTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.run_cli(
            "init", "--server", "https://s", "--tenant-id", "t1", "--default-tenant", "acme", "--package", str(make_archive(self.tmp / "p.tar.gz"))
        )
        self.layout = Layout(self.home / "slop-haus" / "acme")

    def run_cli(self, *argv: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cli.main(list(argv), {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    @contextlib.contextmanager
    def running(self) -> Generator[None]:
        with (self.layout.root / ".run.lock").open("w") as held:
            held.write("4242\n")
            held.flush()
            fcntl.flock(held, fcntl.LOCK_EX)
            yield

    def serve_in_background(self, answers: dict[str, str]) -> threading.Thread:
        spool = ControlSpool(self.layout.control_dir)
        self.requested: list[list[str] | None] = []

        def serve() -> None:
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if spool.serve_pending(lambda names: (self.requested.append(names), answers)[1]):
                    return
                time.sleep(0.01)

        thread = threading.Thread(target=serve, daemon=True)
        thread.start()
        return thread

    def test_status_lists_paused_slots_with_reason_and_age(self) -> None:
        paused_state.write(self.layout.paused_file, [paused_state.PausedSlot("s1", "checkout dirty", time.time() - 7 * 60)])
        with self.running():
            _, out, _ = self.run_cli("status", "-t", "acme")
        self.assertIn("PAUSED s1 for 7m: checkout dirty", out)

    def test_status_ignores_the_paused_file_of_a_supervisor_that_is_gone(self) -> None:
        paused_state.write(self.layout.paused_file, [paused_state.PausedSlot("s1", "checkout dirty", time.time())])
        _, out, _ = self.run_cli("status", "-t", "acme")
        self.assertNotIn("PAUSED", out)

    def test_status_reports_an_unknown_slop_haus_name_in_the_base_run_env(self) -> None:
        (self.layout.root / "run.env").write_text("SLOP_HAUS_FUTURE=on\n")
        _, out, _ = self.run_cli("status", "-t", "acme")
        self.assertIn("run.env: SLOP_HAUS_FUTURE isn't known to this supervisor", out)
        self.assertIn("run `slop-haus update`", out)

    def test_unpause_reaches_the_running_supervisor(self) -> None:
        thread = self.serve_in_background({"s1": ""})
        with self.running():
            code, out, _ = self.run_cli("slot", "unpause", "s1", "-t", "acme")
        thread.join(10)
        self.assertEqual(0, code)
        self.assertIn("s1: unpaused", out)
        self.assertEqual([["s1"]], self.requested)

    def test_unpause_all_asks_for_every_slot(self) -> None:
        thread = self.serve_in_background({"s1": "", "s2": ""})
        with self.running():
            code, _, _ = self.run_cli("slot", "unpause", "--all", "-t", "acme")
        thread.join(10)
        self.assertEqual(0, code)
        self.assertEqual([None], self.requested)

    def test_unpause_says_why_when_the_cause_remains(self) -> None:
        thread = self.serve_in_background({"s1": "the slot checkout has uncommitted changes"})
        with self.running():
            code, out, _ = self.run_cli("slot", "unpause", "s1", "-t", "acme")
        thread.join(10)
        self.assertEqual(1, code)
        self.assertIn("s1: still paused: the slot checkout has uncommitted changes", out)

    def test_unpause_without_a_running_supervisor_is_refused(self) -> None:
        code, _, err = self.run_cli("slot", "unpause", "s1", "-t", "acme")
        self.assertEqual(1, code)
        self.assertIn("not running", err)

    def test_unpause_needs_a_name_or_all(self) -> None:
        code, _, err = self.run_cli("slot", "unpause", "-t", "acme")
        self.assertEqual(1, code)
        self.assertIn("--all", err)
