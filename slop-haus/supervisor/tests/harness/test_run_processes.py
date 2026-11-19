from __future__ import annotations

import os
import signal
import subprocess
import sys
import threading
import unittest
from collections.abc import Callable
import uuid

from process_state import command_line, process_running
from slop_haus_supervisor.harness.run_processes import RUN_TOKEN_ENV, RunProcesses

TOKEN = "run-token"
SETTLES_WITHIN = 10.0
SLEEPER = "/bin/sleep 600"
ROOT = f"""
import subprocess
subprocess.run(["/bin/bash", "-c", "{SLEEPER} & echo $!; wait"])
"""


def marked(pid: int, command: str = "python", token: str = TOKEN) -> bytes:
    return f"{pid} {command} A=1 {RUN_TOKEN_ENV}={token} B=2\n".encode()


def unmarked(pid: int) -> bytes:
    return f"{pid} /bin/sleep 60\n".encode()


def tree(*pairs: tuple[int, int]) -> bytes:
    return "".join(f"{pid} {ppid}\n" for pid, ppid in pairs).encode()


class MarkedAndDescendantsTest(unittest.TestCase):
    """The rule used where `ps -E` hides the environment of Apple platform binaries, on canned `ps` output.

    The canned pids are above any real pid (99999 on macOS, at most 4194304 on Linux): the rule leaves out this
    process, and in a fresh pid namespace, as in CI's containers, a pytest worker's own pid is often in the hundreds.
    """

    def setUp(self) -> None:
        self.processes = RunProcesses(TOKEN)

    def test_a_marked_process_and_everything_below_it_is_the_run(self) -> None:
        environments = marked(9000100) + unmarked(9000101) + unmarked(9000102) + unmarked(9000103)
        parents = tree((9000100, 1), (9000101, 9000100), (9000102, 9000101), (9000103, 9000102))
        self.assertEqual([9000103, 9000102, 9000101, 9000100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_children_come_before_their_parents(self) -> None:
        parents = tree((9000100, 1), (9000101, 9000100), (9000102, 9000100), (9000103, 9000101))
        found = self.processes.marked_and_descendants(marked(9000100) + unmarked(9000101), parents, {})
        self.assertEqual([9000100], found[-1:])
        self.assertLess(found.index(9000103), found.index(9000101))

    def test_a_process_reparented_away_is_not_found_by_the_tree(self) -> None:
        environments = marked(9000100) + unmarked(9000101)
        parents = tree((9000100, 1), (9000101, 1))
        self.assertEqual([9000100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_other_runs_and_unrelated_processes_are_left_alone(self) -> None:
        environments = marked(9000100) + marked(9000200, token="other") + marked(9000300, token=TOKEN + "x") + unmarked(9000400)
        parents = tree((9000100, 1), (9000200, 1), (9000201, 9000200), (9000300, 1), (9000400, 1))
        self.assertEqual([9000100], self.processes.marked_and_descendants(environments, parents, {}))

    def test_this_process_is_never_found_even_below_a_marked_one(self) -> None:
        me = os.getpid()
        parents = tree((9000100, 1), (me, 9000100))
        self.assertEqual([9000100], self.processes.marked_and_descendants(marked(9000100), parents, {}))

    def test_a_parent_loop_does_not_hang_the_walk(self) -> None:
        parents = tree((9000100, 9000101), (9000101, 9000100))
        self.assertEqual([9000101, 9000100], self.processes.marked_and_descendants(marked(9000100), parents, {}))

    def test_a_process_seen_before_is_still_the_run_once_orphaned(self) -> None:
        self.processes.remembered = {9000200: b"Thu Nov 19 07:00:00 2026"}
        parents = tree((9000100, 1), (9000200, 1), (9000201, 9000200))
        starts = {9000200: b"Thu Nov 19 07:00:00 2026"}
        self.assertEqual([9000201, 9000200, 9000100], self.processes.marked_and_descendants(marked(9000100), parents, starts))

    def test_a_pid_reused_by_a_newer_process_is_not_the_run(self) -> None:
        self.processes.remembered = {9000200: b"Thu Nov 19 07:00:00 2026"}
        parents = tree((9000100, 1), (9000200, 1))
        starts = {9000200: b"Thu Nov 19 07:05:00 2026"}
        self.assertEqual([9000100], self.processes.marked_and_descendants(marked(9000100), parents, starts))

    def test_garbage_lines_are_skipped(self) -> None:
        parents = b"PID PPID\n\nnot a line\n9000101 9000100\n"
        self.assertEqual([9000101, 9000100], self.processes.marked_and_descendants(marked(9000100), parents, {}))


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
        # A token of its own: pytest runs tests in parallel, and kill_all ends everything carrying the token.
        token = uuid.uuid4().hex
        run = RunProcesses(token)
        env = {**os.environ, RUN_TOKEN_ENV: token}
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
        # `$!` is printed as soon as bash forks. Until the child's exec of sleep is done, Linux shows the environment
        # of the bash it was forked from and then, mid-exec, none at all, so a look could miss it.
        self.assertTrue(self._within(lambda: command_line(sleeper) == SLEEPER), "the background command never started")
        self.assertIn(sleeper, run.find())
        self.assertTrue(run.carries(sleeper))
        self.assertFalse(run.carries(os.getpid()))
        # Stopping the root first, as the harness does with its process group, must not lose what it started.
        os.kill(shell.pid, signal.SIGKILL)
        shell.wait()
        self.assertIn(sleeper, run.find())
        run.kill_all()
        self.assertTrue(self._within(lambda: not process_running(sleeper)), "the background command survived kill_all")
        self.assertNotIn(sleeper, run.find())

    @staticmethod
    def _kill_group(pgid: int) -> None:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    @staticmethod
    def _within(condition: Callable[[], bool]) -> bool:
        settled = threading.Event()
        for _ in range(int(SETTLES_WITHIN * 200)):
            if condition():
                return True
            settled.wait(0.005)
        return False


if __name__ == "__main__":
    unittest.main()
