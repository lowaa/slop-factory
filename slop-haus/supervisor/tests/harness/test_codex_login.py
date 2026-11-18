"""Several Codex slots on one ChatGPT login, against a provider that rotates refresh tokens and detects reuse.

The "Codex" that refreshes on its own is modelled by `NaiveCodex`, not driven as the real binary: what the real one does
under concurrent refresh is the soak's question. These tests show which schemes survive the worst case.
"""

from __future__ import annotations

import json
import logging
import threading
import unittest
import tempfile
from collections.abc import Callable, Mapping
from typing import Any
from contextlib import AbstractContextManager, nullcontext
from datetime import datetime, timezone
from pathlib import Path

from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.codex_login import REFRESH_AFTER, CodexLogin, file_lock, Locker, CodexSlotGuard, LoginRefreshError, post_json
from slop_haus_supervisor.service.run_env import CODEX_SHARED_LOGIN
from fakes import FakeClock
from fake_auth_server import FakeAuthServer

SLOTS = 4
LOG = logging.getLogger("test")


def write_login(home: Path, refresh_token: str, at: float) -> None:
    home.mkdir(parents=True, exist_ok=True)
    tokens = {"id_token": "id-0", "access_token": "access-0", "refresh_token": refresh_token}
    auth = {"auth_mode": "chatgpt", "tokens": tokens, "last_refresh": datetime.fromtimestamp(at, timezone.utc).isoformat()}
    (home / "auth.json").write_text(json.dumps(auth))


def refresh_token_in(home: Path) -> str:
    return json.loads((home / "auth.json").read_text())["tokens"]["refresh_token"]


def run_together(count: int, work: Callable[[int], object]) -> list[BaseException | None]:
    """Runs work(i) on `count` threads and returns what each raised."""
    errors: list[BaseException | None] = [None] * count

    def target(i: int) -> None:
        try:
            work(i)
        except BaseException as err:  # noqa: BLE001
            errors[i] = err

    threads = [threading.Thread(target=target, args=(i,)) for i in range(count)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return errors


class NaiveCodex:
    """A process that refreshes its home's login itself: read, present the refresh token, write the answer back."""

    def __init__(self, home: Path, url: str, arrived: threading.Barrier) -> None:
        self.home, self.url, self.arrived = home, url, arrived

    def refresh(self) -> None:
        token = refresh_token_in(self.home)
        self.arrived.wait(timeout=10)
        answer = post_json(self.url, {"refresh_token": token})
        auth = json.loads((self.home / "auth.json").read_text())
        auth["tokens"].update({k: v for k, v in answer.items() if k in auth["tokens"]})
        (self.home / "auth.json").write_text(json.dumps(auth))


def no_lock(_path: Path) -> AbstractContextManager[None]:
    return nullcontext()


class LoginCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.server = FakeAuthServer()
        self.addCleanup(self.server.close)
        self.clock = FakeClock()


class SharedHomeTest(LoginCase):
    def test_codex_processes_refreshing_one_shared_home_log_everyone_out(self) -> None:
        home = self.root / "shared"
        write_login(home, self.server.initial_refresh_token, self.clock.now())
        arrived = threading.Barrier(SLOTS)

        errors = run_together(SLOTS, lambda i: NaiveCodex(home, self.server.url, arrived).refresh())

        self.assertTrue(self.server.revoked)
        self.assertEqual(1, self.server.rotations)
        self.assertEqual(SLOTS - 1, len([e for e in errors if isinstance(e, LoginRefreshError)]))
        # The family is dead, so even the process that won holds a token the provider no longer honours.
        self.assertIsNone(self.server.refresh(refresh_token_in(home)))


class RefresherTest(LoginCase):
    def login(self, *, lock: Locker | None = None, hook: threading.Barrier | None = None) -> CodexLogin:
        def post(url: str, body: Mapping[str, str]) -> Mapping[str, Any]:
            if hook is not None:
                # Holds every caller that has read the master until all have, to make the race certain without a lock.
                try:
                    hook.wait(timeout=hook_timeout)
                except threading.BrokenBarrierError:
                    pass
            return post_json(url, body)

        hook_timeout = 5.0 if lock is not None else 0.2
        return CodexLogin(self.root / "master", self.clock, LOG, token_url=self.server.url, post=post, lock=lock or file_lock)

    def copies(self) -> list[Path]:
        return [self.root / f"slot{i}" for i in range(SLOTS)]

    def setup_master(self) -> None:
        write_login(self.root / "master", self.server.initial_refresh_token, self.clock.now())
        self.clock.advance(REFRESH_AFTER + 1)

    def test_concurrent_slots_cause_one_rotation_and_every_copy_follows(self) -> None:
        self.setup_master()
        login = self.login(hook=threading.Barrier(SLOTS))

        errors = run_together(SLOTS, lambda i: login.refresh_if_due(self.copies()))

        self.assertEqual([None] * SLOTS, errors)
        self.assertFalse(self.server.revoked)
        self.assertEqual(1, self.server.rotations)
        self.assertEqual({"refresh-2"}, {refresh_token_in(c) for c in self.copies()} | {refresh_token_in(self.root / "master")})

    def test_independent_instances_share_the_lock_through_the_file(self) -> None:
        self.setup_master()
        arrived = threading.Barrier(SLOTS)

        def refresh(_i: int) -> None:
            # Each caller builds its own, as separate supervisor processes would.
            hook = threading.Barrier(1)
            login = self.login(hook=hook)
            arrived.wait(timeout=10)
            login.refresh_if_due(self.copies())

        errors = run_together(SLOTS, refresh)

        self.assertEqual([None] * SLOTS, errors)
        self.assertEqual(1, self.server.rotations)
        self.assertFalse(self.server.revoked)

    def test_the_login_survives_expiry_after_expiry(self) -> None:
        self.setup_master()
        login = self.login()
        for rotation in (1, 2, 3):
            self.assertTrue(login.refresh_if_due(self.copies()))
            self.assertFalse(login.refresh_if_due(self.copies()))
            self.clock.advance(REFRESH_AFTER + 1)
            self.assertEqual(rotation, self.server.rotations)
        self.assertFalse(self.server.revoked)

    def test_without_the_lock_the_same_race_revokes_the_login(self) -> None:
        self.setup_master()
        login = self.login(lock=no_lock, hook=threading.Barrier(SLOTS))

        errors = run_together(SLOTS, lambda i: login.refresh_if_due(self.copies()))

        self.assertTrue(self.server.revoked)
        self.assertTrue(any(isinstance(e, LoginRefreshError) for e in errors))

    def test_a_refused_refresh_leaves_the_master_and_copies_alone(self) -> None:
        write_login(self.root / "master", "never-issued", self.clock.now())
        write_login(self.root / "slot0", "copy-token", self.clock.now())
        self.clock.advance(REFRESH_AFTER + 1)

        with self.assertRaises(LoginRefreshError) as caught:
            self.login().refresh_if_due([self.root / "slot0"])

        self.assertNotIn("never-issued", str(caught.exception))
        self.assertEqual("never-issued", refresh_token_in(self.root / "master"))
        self.assertEqual("copy-token", refresh_token_in(self.root / "slot0"))

    def test_a_young_login_is_copied_without_asking_the_provider(self) -> None:
        write_login(self.root / "master", self.server.initial_refresh_token, self.clock.now())

        self.assertFalse(self.login().refresh_if_due(self.copies()))

        self.assertEqual(0, self.server.rotations)
        self.assertEqual(self.server.initial_refresh_token, refresh_token_in(self.copies()[0]))
        self.assertEqual(0o600, (self.copies()[0] / "auth.json").stat().st_mode & 0o777)


class WiredSlotsTest(LoginCase):
    def harness(self, login: CodexLogin | None) -> CodexHarness:
        return CodexHarness(self.root, "https://s/", environ={}, login=login)

    def launch_env(self, harness: CodexHarness, agent: str) -> dict[str, str]:
        return harness.plan(RunSpec(agent, self.root, 1, "b", "p", "", "")).env

    def test_two_slots_launched_across_an_expiry_each_get_a_current_copy_and_nothing_is_revoked(self) -> None:
        master = self.root / "harness" / "codex"
        write_login(master, self.server.initial_refresh_token, self.clock.now())
        arrived = threading.Barrier(2)

        def post(url: str, body: Mapping[str, str]) -> Mapping[str, Any]:
            # Holds the first presenter until the second launch has started, so the lock is what keeps it from presenting too.
            try:
                arrived.wait(timeout=0.5)
            except threading.BrokenBarrierError:
                pass
            return post_json(url, body)

        login = CodexLogin(master, self.clock, LOG, token_url=self.server.url, post=post)
        harness = self.harness(login)
        self.clock.advance(REFRESH_AFTER + 1)

        errors = run_together(2, lambda i: self.launch_env(harness, f"slot{i}"))

        self.assertEqual([None, None], errors)
        self.assertFalse(self.server.revoked)
        self.assertEqual(1, self.server.rotations)
        homes = [Path(self.launch_env(harness, f"slot{i}")["CODEX_HOME"]) for i in range(2)]
        self.assertNotEqual(homes[0], homes[1])
        self.assertNotIn(master, homes)
        self.assertEqual({"refresh-2"}, {refresh_token_in(h) for h in homes})

    def test_a_refused_refresh_fails_the_launch_and_makes_no_copy(self) -> None:
        master = self.root / "harness" / "codex"
        write_login(master, "never-issued", self.clock.now())
        self.clock.advance(REFRESH_AFTER + 1)
        harness = self.harness(CodexLogin(master, self.clock, LOG, token_url=self.server.url))

        with self.assertRaises(LoginRefreshError):
            self.launch_env(harness, "slot0")

        self.assertFalse((self.root / "harness" / "codex-slots" / "slot0").exists())

    def test_without_a_shared_login_every_slot_uses_the_one_home(self) -> None:
        harness = self.harness(None)

        self.assertEqual(str(self.root / "harness" / "codex"), self.launch_env(harness, "slot0")["CODEX_HOME"])
        self.assertEqual(str(self.root / "harness" / "codex"), self.launch_env(harness, "slot1")["CODEX_HOME"])


class SlotGuardTest(unittest.TestCase):
    def guard(self, environ: dict[str, str]) -> tuple[CodexSlotGuard, list[str]]:
        records: list[str] = []

        class Collect(logging.Handler):
            def emit(self, record: logging.LogRecord) -> None:
                records.append(record.getMessage())

        logger = logging.Logger("guard-test")
        logger.addHandler(Collect())
        return CodexSlotGuard(environ, logger), records

    def test_one_slot_needs_no_flag(self) -> None:
        guard, records = self.guard({})
        self.assertTrue(guard.allows(1))
        self.assertEqual([], records)

    def test_a_second_slot_is_refused_with_the_reason(self) -> None:
        guard, records = self.guard({})
        self.assertFalse(guard.allows(2))
        self.assertIn(CODEX_SHARED_LOGIN, records[0])
        self.assertIn("unproven", records[0])

    def test_only_the_word_verified_lifts_it(self) -> None:
        self.assertFalse(self.guard({CODEX_SHARED_LOGIN: "1"})[0].allows(2))
        self.assertTrue(self.guard({CODEX_SHARED_LOGIN: "verified"})[0].allows(3))
