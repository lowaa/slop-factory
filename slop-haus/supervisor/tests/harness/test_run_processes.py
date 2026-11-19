from __future__ import annotations

import os
import signal
import subprocess
import sys
import threading
import unittest

from process_state import process_running
from slop_haus_supervisor.harness.run_processes import RUN_TOKEN_ENV, RunProcesses

TOKEN = "run-token"
GONE_WITHIN = 10.0
ROOT = """
import subprocess
subprocess.run(["/bin/bash", "-c", "/bin/sleep 600 & echo $!; wait"])
"""


def marked(pid: int, command: str = "python", token: str = TOKEN) -> bytes:
    return f"{pid} {command} A=1 {RUN_TOKEN_ENV}={token} B=2\n".encode()


def unmarked(pid: int) -> bytes:
    return f"{pid} /bin/sleep 60\n".encode()


def tree(*pairs: tuple[int, int]) -> bytes:
    return "".join(f"{pid} {ppid}\n" for pid, ppid in pairs).encode()


class MarkedAndDescendantsTest(unittest.TestCase):
    """The rule used where `ps -E` hides the environment of Apple platform binaries, on canned `ps` output."""

    def setUp(self) -> None:
        self.processes = RunProcesses(TOKEN)

    def test_a_marked_process_and_everything_below_it_is_the_run(self) -> None:
        environments = marked(100) + unmarked(101) + unmarked(102) + unmarked(103)
        parents = tree((100, 1), (101, 100), (102, 101), (103, 102))
        self.assertEqual([103, 102, 101, 100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_children_come_before_their_parents(self) -> None:
        parents = tree((100, 1), (101, 100), (102, 100), (103, 101))
        found = self.processes.marked_and_descendants(marked(100) + unmarked(101), parents, {})
        self.assertEqual([100], found[-1:])
        self.assertLess(found.index(103), found.index(101))

    def test_a_process_reparented_away_is_not_found_by_the_tree(self) -> None:
        environments = marked(100) + unmarked(101)
        parents = tree((100, 1), (101, 1))
        self.assertEqual([100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_other_runs_and_unrelated_processes_are_left_alone(self) -> None:
        environments = marked(100) + marked(200, token="other") + marked(300, token=TOKEN + "x") + unmarked(400)
        parents = tree((100, 1), (200, 1), (201, 200), (300, 1), (400, 1))
        self.assertEqual([100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_this_process_is_never_found_even_below_a_marked_one(self) -> None:
        me = os.getpid()
        parents = tree((100, 1), (me, 100))
        self.assertEqual([100], self.processes.marked_and_descendants(marked(100), parents, {}))

    def test_a_parent_loop_does_not_hang_the_walk(self) -> None:
        parents = tree((100, 101), (101, 100))
        self.assertEqual([101, 100], self.processes.marked_and_descendants(marked(100), parents, {}))

    def test_a_process_seen_before_is_still_the_run_once_orphaned(self) -> None:
        self.processes.remembered = {200: b"Thu Nov 19 07:00:00 2026"}
        parents = tree((100, 1), (200, 1), (201, 200))
        starts = {200: b"Thu Nov 19 07:00:00 2026"}
        self.assertEqual([201, 200, 100], self.processes.marked_and_descendants(marked(100), parents, starts))

    def test_a_pid_reused_by_a_newer_process_is_not_the_run(self) -> None:
        self.processes.remembered = {200: b"Thu Nov 19 07:00:00 2026"}
        parents = tree((100, 1), (200, 1))
        starts = {200: b"Thu Nov 19 07:05:00 2026"}
        self.assertEqual([100], self.processes.marked_and_descendants(marked(100), parents, starts))

    def test_garbage_lines_are_skipped(self) -> None:
        parents = b"PID PPID\n\nnot a line\n101 100\n"
        self.assertEqual([101, 100], self.processes.marked_and_descendants(marked(100), parents, {}))


@unittest.skipIf(os.path.isdir("/proc/self"), "the re-check by start time is the path for hosts without /proc")
class CarriesWithoutProcTest(unittest.TestCase):
    def setUp(self) -> None:
        self.processes = RunProcesses(TOKEN)
        self.pid = os.getppid()
        self.start = subprocess.run(["ps", "-o", "lstart=", "-p", str(self.pid)], capture_output=True, check=True).stdout.strip()

    def test_a_remembered_process_with_the_same_start_time_is_the_runs(self) -> None:
        self.processes.remembered = {self.pid: self.start}
        self.assertTrue(self.processes.carries(self.pid))

    def test_a_pid_now_held_by_a_process_that_started_at_another_time_is_not(self) -> None:
        self.processes.remembered = {self.pid: b"Thu Jan  1 00:00:00 1970"}
        self.assertFalse(self.processes.carries(self.pid))

    def test_a_pid_never_seen_is_not(self) -> None:
        self.assertFalse(self.processes.carries(self.pid))


class RealProcessesTest(unittest.TestCase):
    """Against real processes, whichever way this host finds them (/proc on Linux, `ps` on macOS)."""

    def test_a_background_command_of_a_shell_the_run_started_is_found_and_killed(self) -> None:
        self._find_and_kill_a_background_command()

    def test_without_pidfd_as_on_uvs_standalone_linux_pythons_it_still_kills(self) -> None:
        # The supervisor runs on uv's Python, whose Linux builds have neither function; a host Python usually has both.
        for module, name in ((os, "pidfd_open"), (signal, "pidfd_send_signal")):
            if hasattr(module, name):
                self.addCleanup(setattr, module, name, getattr(module, name))
                delattr(module, name)
        self._find_and_kill_a_background_command()

    def _find_and_kill_a_background_command(self) -> None:
        run = RunProcesses(TOKEN)
        env = {**os.environ, RUN_TOKEN_ENV: TOKEN}
        # The root is Python, as the harness's real child (claude, pi, codex) is: an Apple platform binary such as
        # bash shows no environment to `ps -E`, so it could not be the one marked.
        # A session of its own, as the harness's process is: the shell and its background `sleep` share the group.
        shell = subprocess.Popen([sys.executable, "-c", ROOT], env=env, stdout=subprocess.PIPE, text=True, start_new_session=True)
        assert shell.stdout is not None
        # LIFO, so both kills run before the wait. The group kill does not depend on the code under test: if find()
        # regressed, kill_all would end nothing and the wait would hang instead of the test failing. A group, not
        # the pids: a pid kill_all already ended may belong to another process by then; the group is gone with them.
        self.addCleanup(shell.stdout.close)
        self.addCleanup(shell.wait)
        self.addCleanup(self._kill_group, shell.pid)
        self.addCleanup(run.kill_all)
        sleeper = int(shell.stdout.readline())
        self.assertIn(sleeper, run.find())
        self.assertTrue(run.carries(sleeper))
        self.assertFalse(run.carries(os.getpid()))
        # Stopping the root first, as the harness does with its process group, must not lose what it started.
        os.kill(shell.pid, signal.SIGKILL)
        shell.wait()
        self.assertIn(sleeper, run.find())
        run.kill_all()
        self.assertTrue(self._gone(sleeper), "the background command survived kill_all")
        self.assertNotIn(sleeper, run.find())

    @staticmethod
    def _kill_group(pgid: int) -> None:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    @staticmethod
    def _gone(pid: int) -> bool:
        settled = threading.Event()
        for _ in range(int(GONE_WITHIN * 200)):
            if not process_running(pid):
                return True
            settled.wait(0.005)
        return False


if __name__ == "__main__":
    unittest.main()
