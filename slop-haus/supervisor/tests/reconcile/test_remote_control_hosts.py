from __future__ import annotations

import logging
from collections.abc import Mapping

from fakes import FakeAlerts, FakeClock
from reconcile_support import FakeHost, ReconcilerTestCase, entry
from slop_haus_supervisor.fleet.host_retirement import HostRetirement
from slop_haus_supervisor.fleet.host_slot import CRASH_LOOP, IDLE_MARKER, RemoteControlDeps, RemoteControlSlot
from slop_haus_supervisor.fleet.phase import Phase
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.reconcile.ledger import ResumeLedger
from slop_haus_supervisor.reconcile.reconciler import Reconciler
from slop_haus_supervisor.workspace.host_files import host_lock
from test_host_slot import CONFIG, FakeSpawner, FakeWorkspace


class FakeRetirer:
    def __init__(self) -> None:
        self.retired: list[str] = []

    def retire_agent(self, name: str) -> None:
        self.retired.append(name)


class HostFleet(FakeHost):
    """The scheduler's part, done by hand: real host slots, stepped when the test says."""

    def __init__(self, test: "RemoteControlHostsTest") -> None:
        super().__init__()
        self.test = test
        self.slots: dict[str, RemoteControlSlot] = {}

    def start_host(self, name: str, harness: str, held: bool) -> None:
        if harness not in registry.REMOTE_CONTROL_NAMES:
            raise ConfigError(f"the {harness} harness is not available for Remote Control hosts")
        super().start_host(name, harness, held)
        t = self.test
        deps = RemoteControlDeps(
            t.layout, FakeWorkspace(t.layout.remote_control_workspace(name)), t.spawner, t.clock, FakeAlerts(),
            lambda _harness, _secrets: "", {"PATH": "/usr/bin"}, logging.getLogger("test"), config=CONFIG,
        )
        self.slots[name] = RemoteControlSlot(name, harness, deps)
        self.slots[name].set_held(held)
        t.addCleanup(self.slots[name].abandon_run)

    def hold(self, name: str, held: bool) -> None:
        super().hold(name, held)
        if name in self.slots:
            self.slots[name].set_held(held)

    def pause_reason(self, name: str) -> str | None:
        if name not in self.slots:
            return None
        info = self.slots[name].pause_info()
        return info[0] if info is not None else None

    def unpause(self, names: list[str] | None) -> Mapping[str, str]:
        return {n: self.slots[n].try_unpause(manual=True) for n in names or []}

    def retire(self, name: str) -> bool:
        slot = self.slots.get(name)
        if slot is None:
            return True
        slot.begin_drain()
        slot.step()
        if slot.phase == Phase.RUNNING:
            return False
        del self.slots[name]
        self.names.discard(name)
        return True


class RemoteControlHostsTest(ReconcilerTestCase):
    def setUp(self) -> None:
        self.spawner = FakeSpawner()
        self.clock = FakeClock()
        self.retirer = FakeRetirer()
        super().setUp()
        self.fleet = HostFleet(self)
        self.host = self.fleet
        self.reconciler = self.build()
        self.directory = self.layout.remote_control_workspace("rc1")

    def build(self) -> Reconciler:
        return Reconciler(
            self.layout, self.api, self.host, self.repo, ResumeLedger(self.layout.state_dir / "reconcile.json"),
            logging.getLogger("test"), lambda: self.now, lambda argv, cwd: self._run(argv),
            host_retirement=HostRetirement(self.layout, self.retirer, self.repo, logging.getLogger("test")),
        )

    def rc(self, **over: object) -> dict[str, object]:
        return entry("rc1", kind="remote_control", launch="supervisor", **over)

    def phases(self) -> list[object]:
        return [r["phase"] for batch in self.api.reports for r in batch]

    def test_a_supervisor_launched_entry_starts_a_host(self) -> None:
        self.want(self.rc(harness="pi"))

        self.reconciler.tick()
        self.fleet.slots["rc1"].step()

        self.assertEqual([("rc1", "pi")], self.fleet.hosts_started)
        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual(1, len(self.spawner.spawned))
        self.assertIn("--supervised", self.spawner.spawned[0][0])
        self.assertEqual([], self.api.calls, "no worker slot is made for it")
        self.assertFalse(self.layout.slot("rc1").exists())

    def test_an_entry_is_started_once(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        self.reconciler.tick()

        self.assertEqual([("rc1", "claude")], self.fleet.hosts_started)

    def test_a_manual_entry_is_left_alone(self) -> None:
        for launch in ("manual", ""):
            self.want(entry("rc1", kind="remote_control", launch=launch))
            self.reconciler.tick()

            self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual([], self.fleet.hosts_started)
        self.assertEqual([], self.spawner.spawned)

    def test_an_entry_without_launch_is_the_owners(self) -> None:
        self.want(entry("rc1", kind="remote_control"))

        self.reconciler.tick()

        self.assertEqual([], self.fleet.hosts_started)

    def test_an_unknown_launch_is_refused(self) -> None:
        self.want(entry("rc1", kind="remote_control", launch="cloud"))

        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertEqual([], self.fleet.hosts_started)

    def test_a_host_that_dies_is_started_again_after_the_backoff(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        slot = self.fleet.slots["rc1"]
        slot.step()
        self.clock.advance(120)
        self.spawner.last.code = 1
        slot.step()
        self.assertEqual(Phase.BACKOFF, slot.phase)

        self.clock.advance(CONFIG.backoffs[0])
        slot.step()

        self.assertEqual(Phase.RUNNING, slot.phase)
        self.assertEqual(2, len(self.spawner.spawned))

    def test_a_crash_loop_is_reported_paused_with_its_cause(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        slot = self.fleet.slots["rc1"]
        for _ in range(CONFIG.crash_loop_failures):
            slot.step()
            self.spawner.last.code = 1
            slot.step()
            self.clock.advance(1000)
            slot.step()
            if slot.phase == Phase.PAUSED:
                break

        self.reconciler.tick()

        report = self.api.last_phase()
        self.assertEqual("paused", report["phase"])
        self.assertTrue(str(report["reason"]).startswith(CRASH_LOOP))

    def test_a_host_started_by_hand_pauses_the_slot_with_the_documented_reason(self) -> None:
        self.want(self.rc())
        (self.directory / ".slop-haus").mkdir(parents=True)
        self.reconciler.tick()

        with host_lock(self.directory):
            self.fleet.slots["rc1"].step()
            self.reconciler.tick()

        report = self.api.last_phase()
        self.assertEqual("paused", report["phase"])
        self.assertIn("a Remote Control host started by hand is running", str(report["reason"]))
        self.assertEqual([], self.spawner.spawned)

    def test_the_owners_hold_stops_the_host_between_sessions(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        slot = self.fleet.slots["rc1"]
        slot.step()

        self.want(self.rc(state="paused", generation=2))
        self.reconciler.tick()
        (self.directory / IDLE_MARKER).parent.mkdir(parents=True, exist_ok=True)
        (self.directory / IDLE_MARKER).write_text("idle\n")
        slot.step()

        self.assertEqual("paused", self.api.last_phase()["phase"])
        self.assertEqual(Phase.IDLE, slot.phase)
        self.assertEqual(-15, self.spawner.last.code)

    def test_removal_waits_for_a_live_session_then_retires_and_deletes(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        slot = self.fleet.slots["rc1"]
        slot.step()
        self.directory.mkdir(parents=True, exist_ok=True)
        (self.directory / "file").write_text("x")
        marker = self.directory / IDLE_MARKER
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text("busy\n")

        self.want(self.rc(state="removed", generation=2))
        self.reconciler.tick()

        self.assertEqual("provisioning", self.api.last_phase()["phase"])
        self.assertEqual([], self.retirer.retired)
        self.assertTrue(self.directory.exists())

        marker.write_text("idle\n")
        self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase()["phase"])
        self.assertEqual(["rc1"], self.retirer.retired)
        self.assertFalse(self.directory.exists())
        self.assertEqual(-15, self.spawner.last.code)

    def test_a_removed_manual_entry_is_retired_without_touching_anything(self) -> None:
        self.directory.mkdir(parents=True)
        self.want(entry("rc1", kind="remote_control", launch="manual", state="removed"))

        self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase()["phase"])
        self.assertEqual([], self.retirer.retired)
        self.assertTrue(self.directory.exists())

    def test_a_reconciler_without_host_retirement_leaves_every_host_to_the_owner(self) -> None:
        self.reconciler = Reconciler(
            self.layout, self.api, self.host, self.repo, ResumeLedger(self.layout.state_dir / "reconcile.json"),
            logging.getLogger("test"), lambda: self.now,
        )
        self.want(self.rc())

        self.reconciler.tick()

        self.assertEqual([], self.fleet.hosts_started)

    def test_a_launch_of_supervisor_means_nothing_to_a_worker_or_a_steward(self) -> None:
        self.want(entry("w1", launch="supervisor"))
        self.reconciler.tick()

        self.assertEqual([], self.fleet.hosts_started)
        self.assertEqual(["create w1"], self.api.calls)

        self.want(entry("steward", kind="steward", launch="supervisor", id="22222222-2222-4222-8222-222222222222"))
        self.reconciler.tick()

        self.assertEqual([], self.fleet.hosts_started)

    def test_a_harness_a_host_cannot_run_is_reported_failed_and_not_retried_at_once(self) -> None:
        self.want(self.rc(harness="emacs"))

        self.reconciler.tick()
        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertEqual([], self.fleet.hosts_started)

    def test_handing_the_host_back_to_the_owner_ends_the_one_the_supervisor_started(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        slot = self.fleet.slots["rc1"]
        slot.step()
        marker = self.directory / IDLE_MARKER
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text("busy\n")

        self.want(entry("rc1", kind="remote_control", launch="manual", generation=2))
        self.reconciler.tick()

        self.assertEqual("provisioning", self.api.last_phase()["phase"])
        self.assertIsNone(self.spawner.last.code)

        marker.write_text("idle\n")
        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual(-15, self.spawner.last.code)
        self.assertNotIn("rc1", self.fleet.names)
        self.assertTrue(self.directory.exists(), "the owner keeps the workspace")

    def test_a_changed_harness_restarts_the_host_after_the_session(self) -> None:
        self.want(self.rc())
        self.reconciler.tick()
        self.fleet.slots["rc1"].step()
        marker = self.directory / IDLE_MARKER
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text("busy\n")

        self.want(self.rc(harness="pi", generation=2))
        self.reconciler.tick()
        self.assertEqual([("rc1", "claude")], self.fleet.hosts_started)

        marker.write_text("idle\n")
        self.reconciler.tick()

        self.assertEqual([("rc1", "claude"), ("rc1", "pi")], self.fleet.hosts_started)
        self.assertEqual("pi", self.fleet.slots["rc1"].harness)

    def test_a_workspace_a_manual_host_still_holds_is_not_deleted_on_removal(self) -> None:
        self.want(self.rc())
        (self.directory / ".slop-haus").mkdir(parents=True)
        (self.directory / "file").write_text("x")
        self.reconciler.tick()

        with host_lock(self.directory):
            self.fleet.slots["rc1"].step()
            self.want(self.rc(state="removed", generation=2))
            self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase()["phase"])
        self.assertTrue((self.directory / "file").exists())

    def test_a_host_added_on_hold_never_starts_a_session(self) -> None:
        self.want(self.rc(state="paused"))

        self.reconciler.tick()
        self.fleet.slots["rc1"].step()

        self.assertEqual([], self.spawner.spawned)
        self.assertEqual("paused", self.api.last_phase()["phase"])
