from __future__ import annotations

import json
import stat
import subprocess
import tempfile
import unittest
import unittest.mock
from collections.abc import Generator
from contextlib import contextmanager
from pathlib import Path

from slop_haus_supervisor.slophaus.credentials import TokenPair, credentials_lock, write_credentials
from slop_haus_supervisor.workspace.config import ConfigError


class WriteCredentialsTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def test_writes_the_shape_token_sh_reads_and_keeps_it_private(self) -> None:
        write_credentials(self.dir, "https://s.example/", TokenPair("acc", "ref", 1900))

        state = self.dir / ".slop-haus"
        self.assertEqual({"access_token": "acc", "refresh_token": "ref", "expires_at": 1900}, json.loads((state / "credentials.json").read_text()))
        self.assertEqual("https://s.example\n", (state / "server-url").read_text())
        self.assertEqual(0o600, stat.S_IMODE((state / "credentials.json").stat().st_mode))
        self.assertEqual(0o700, stat.S_IMODE(state.stat().st_mode))

    def test_rewriting_stays_private_and_leaves_no_temp_files(self) -> None:
        write_credentials(self.dir, "https://s", TokenPair("a", "r", 1))
        write_credentials(self.dir, "https://s", TokenPair("b", "r2", 2))

        state = self.dir / ".slop-haus"
        self.assertEqual("b", json.loads((state / "credentials.json").read_text())["access_token"])
        self.assertEqual(["credentials.json", "credentials.lock", "server-url"], sorted(p.name for p in state.iterdir()))

    def test_a_symlinked_credentials_file_is_replaced_not_written_through(self) -> None:
        (self.dir / ".slop-haus").mkdir()
        target = self.dir / "elsewhere"
        target.write_text("keep")
        link = self.dir / ".slop-haus" / "credentials.json"
        link.symlink_to(target)

        write_credentials(self.dir, "https://s", TokenPair("a", "r", 1))

        self.assertEqual("keep", target.read_text())
        self.assertFalse(link.is_symlink())
        self.assertEqual("a", json.loads(link.read_text())["access_token"])

    def test_a_symlinked_state_directory_is_refused(self) -> None:
        elsewhere = self.dir / "elsewhere"
        elsewhere.mkdir()
        (self.dir / ".slop-haus").symlink_to(elsewhere)

        with self.assertRaises(ConfigError):
            write_credentials(self.dir, "https://s", TokenPair("a", "r", 1))

        self.assertEqual([], list(elsewhere.iterdir()))


@contextmanager
def bash_flock_holder(lock: Path) -> Generator[None]:
    """A separate process holding `lock` the way token.sh does (`exec 9> lock; flock 9`) until the block ends."""
    lock.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    holder = subprocess.Popen(
        ["bash", "-c", f'exec 9> "{lock}"; flock 9; echo held; read _'],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
    )  # fmt: skip
    assert holder.stdout is not None and holder.stdin is not None
    try:
        assert holder.stdout.readline().strip() == "held"
        yield
    finally:
        holder.stdin.close()
        holder.wait()
        holder.stdout.close()


class CredentialsLockTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        self.lock = self.dir / ".slop-haus" / "credentials.lock"
        patcher = unittest.mock.patch("slop_haus_supervisor.slophaus.credentials.LOCK_TIMEOUT", 0.3)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_a_held_lock_times_out_the_writer_and_leaves_the_file_alone(self) -> None:
        write_credentials(self.dir, "https://s", TokenPair("old", "r", 1))

        with bash_flock_holder(self.lock):
            with self.assertRaisesRegex(ConfigError, "timed out"):
                write_credentials(self.dir, "https://s", TokenPair("new", "r2", 2))

        self.assertEqual("old", json.loads((self.dir / ".slop-haus" / "credentials.json").read_text())["access_token"])

    def test_a_writer_waits_for_the_holder_then_proceeds(self) -> None:
        write_credentials(self.dir, "https://s", TokenPair("old", "r", 1))
        released = False

        def release_after_first_poll(_seconds: float) -> None:
            nonlocal released
            released = True
            raise _Released

        # The writer must be seen waiting (sleeping) while the holder has the lock; the holder is then
        # dropped and a second write succeeds.
        with unittest.mock.patch("slop_haus_supervisor.slophaus.slop_haus_token.time.sleep", release_after_first_poll):
            with bash_flock_holder(self.lock):
                with self.assertRaises(_Released):
                    write_credentials(self.dir, "https://s", TokenPair("new", "r2", 2))
        self.assertTrue(released)
        self.assertEqual("old", json.loads((self.dir / ".slop-haus" / "credentials.json").read_text())["access_token"])

        write_credentials(self.dir, "https://s", TokenPair("new", "r2", 2))
        self.assertEqual("new", json.loads((self.dir / ".slop-haus" / "credentials.json").read_text())["access_token"])

    def test_the_lock_excludes_a_bash_flock_while_held(self) -> None:
        with credentials_lock(self.dir):
            held = subprocess.run(["flock", "-n", str(self.lock), "true"], check=False)
        free = subprocess.run(["flock", "-n", str(self.lock), "true"], check=False)

        self.assertNotEqual(0, held.returncode)
        self.assertEqual(0, free.returncode)

    def test_a_symlinked_lock_file_is_refused(self) -> None:
        (self.dir / ".slop-haus").mkdir()
        target = self.dir / "elsewhere"
        self.lock.symlink_to(target)

        with self.assertRaises(ConfigError):
            write_credentials(self.dir, "https://s", TokenPair("a", "r", 1))

        self.assertFalse(target.exists())


class _Released(Exception):
    pass
