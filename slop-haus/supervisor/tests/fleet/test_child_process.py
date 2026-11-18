from __future__ import annotations

import os
import signal
import sys
import tempfile
import time
import unittest
from pathlib import Path

from slop_haus_supervisor.fleet.child_process import SubprocessSpawner

# Ignores TERM, starts a descendant in the same group that also ignores it, and says both pids.
STUBBORN = """
import os, signal, subprocess, sys
signal.signal(signal.SIGTERM, signal.SIG_IGN)
kid = subprocess.Popen([sys.executable, "-c", "import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)"])
with open(sys.argv[1], "w") as report:
    report.write(f"{os.getpid()} {kid.pid}")
import time; time.sleep(60)
"""


def alive(pid: int) -> bool:
    """Running, not merely waiting to be reaped by whatever adopted it."""
    try:
        state = Path(f"/proc/{pid}/stat").read_text().rpartition(")")[2].split()[0]
    except OSError:
        return False
    return state != "Z"


class SubprocessSpawnerTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        # The child says what it has through this, so the test blocks on it rather than polling.
        self.report = self.tmp / "report"
        os.mkfifo(self.report)

    def heard(self) -> str:
        return self.report.read_text()

    def test_the_child_gets_only_the_environment_it_is_given(self) -> None:
        code = "import os, sys; open(sys.argv[1], 'w').write(repr(sorted(os.environ))); import time; time.sleep(60)"
        child = SubprocessSpawner().spawn([sys.executable, "-c", code, str(self.report)], self.tmp, {"ONLY": "1"})
        self.addCleanup(child.terminate, 1)

        # The interpreter adds LC_CTYPE itself.
        self.assertEqual("['ONLY']", self.heard().replace("'LC_CTYPE', ", ""))

    def test_terminate_ends_a_child_that_ignores_term_and_what_it_started(self) -> None:
        child = SubprocessSpawner().spawn([sys.executable, "-c", STUBBORN, str(self.report)], self.tmp, {})
        self.addCleanup(child.terminate, 1)
        parent, kid = (int(p) for p in self.heard().split())

        child.terminate(0.2)

        self.assertEqual(-signal.SIGKILL, child.poll())
        # The grandchild is not ours to wait on and SIGKILL reaches it asynchronously, so give it a bounded time to die.
        deadline = time.monotonic() + 10
        while alive(kid) and time.monotonic() < deadline:
            time.sleep(0.01)
        self.assertFalse(alive(kid))
        self.assertFalse(alive(parent))
