from __future__ import annotations

import io
import logging
import tarfile
import tempfile
import unittest
from collections.abc import Mapping
from pathlib import Path

from slop_haus_supervisor.reconcile.ledger import ResumeLedger
from slop_haus_supervisor.reconcile.reconciler import Reconciler
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout

TOKENS = TokenPair("access", "refresh", 1900)
ID = "11111111-1111-4111-8111-111111111111"


def entry(name: object = "w1", **over: object) -> dict[str, object]:
    base: dict[str, object] = {
        "id": ID, "kind": "slot", "name": name, "harness": "claude", "state": "running", "generation": 1, "resume_generation": 0
    }
    return {**base, **over}


class FakeApi:
    """Serves the document the test sets and records every call; it decides nothing."""

    def __init__(self) -> None:
        self.document: dict[str, object] = {"revision": 1, "agents": []}
        self.reports: list[list[dict[str, object]]] = []
        self.calls: list[str] = []
        self.slots: dict[str, SlotRef] = {}
        self.fail_create = False
        self.fail_message = "boom"

    def desired_state(self) -> dict[str, object]:
        return self.document

    def report_observed(self, reports: list[dict[str, object]]) -> None:
        self.reports.append(reports)

    def create_slot(self, name: str) -> SlotRef:
        self.calls.append(f"create {name}")
        if self.fail_create:
            raise ApiError(self.fail_message, 500)
        return self.slots.setdefault(name, SlotRef(f"id-{name}", name))

    def find_slot(self, name: str) -> SlotRef | None:
        return self.slots.get(name)

    def adopt_slot(self, name: str) -> SlotRef:
        raise AssertionError("not adopted")

    def is_retired(self, name: str) -> bool:
        return False

    def retire_slot(self, slot: SlotRef) -> None:
        self.calls.append(f"retire {slot.name}")
        self.slots.pop(slot.name, None)

    def issue_credential(self, slot: SlotRef) -> TokenPair:
        return TOKENS

    def update_script(self, tokens: TokenPair) -> str:
        return "#!/usr/bin/env bash\n"

    def last_phase(self, agent_id: str = ID) -> dict[str, object]:
        return next(r for batch in reversed(self.reports) for r in batch if r["id"] == agent_id)


class FakeHost:
    def __init__(self) -> None:
        self.names: set[str] = set()
        self.started: list[str] = []
        self.holds: dict[str, bool] = {}
        self.cause: dict[str, str] = {}
        self.unpaused: list[list[str] | None] = []
        self.draining = False
        self.retired: list[str] = []
        self.hosts_started: list[tuple[str, str]] = []
        self.slot_harness: dict[str, str] = {}

    def slot_names(self) -> set[str]:
        return set(self.names)

    def start_slot(self, name: str, harness: str) -> None:
        self.started.append(name)
        self.slot_harness[name] = harness
        self.names.add(name)

    def harness_of(self, name: str) -> str:
        return self.slot_harness[name]

    def start_host(self, name: str, harness: str, held: bool) -> None:
        self.hosts_started.append((name, harness))
        self.slot_harness[name] = harness
        self.names.add(name)

    def hold(self, name: str, held: bool) -> None:
        self.holds[name] = held

    def pause_reason(self, name: str) -> str | None:
        return self.cause.get(name)

    def unpause(self, names: list[str] | None) -> Mapping[str, str]:
        self.unpaused.append(names)
        for name in names or []:
            self.cause.pop(name, None)
        return {}

    def retire(self, name: str) -> bool:
        if self.draining:
            return False
        self.retired.append(name)
        self.names.discard(name)
        return True


class FakeRepo:
    def __init__(self, unpushed: bool = False) -> None:
        self.unpushed = unpushed
        self.asked: list[Path] = []

    def has_unpushed_anywhere(self, directory: Path) -> bool:
        self.asked.append(directory)
        return self.unpushed


def make_archive(path: Path) -> Path:
    files = {"pyproject.toml": b'[project]\nname = "slop-haus-supervisor"\nversion = "0.1.0"\n', "uv.lock": b"", "src/pkg/__init__.py": b""}
    with tarfile.open(path, "w:gz") as tar:
        for name, body in files.items():
            info = tarfile.TarInfo(name)
            info.size = len(body)
            tar.addfile(info, io.BytesIO(body))
    return path


class ReconcilerTestCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.layout = Layout(self.tmp / "acme")
        lay.set_up(self.layout, SupervisorConfig("https://slop-haus.example", "tenant", "acme"), make_archive(self.tmp / "p.tar.gz"))
        self.api, self.host, self.repo = FakeApi(), FakeHost(), FakeRepo()
        self.ran: list[list[str]] = []
        self.now: float = 1000.0
        self.reconciler = self.build()

    def build(self) -> Reconciler:
        return Reconciler(
            self.layout, self.api, self.host, self.repo, ResumeLedger(self.layout.state_dir / "reconcile.json"),
            logging.getLogger("test"), lambda: self.now, lambda argv, cwd: self._run(argv),
        )

    def _run(self, argv: list[str]) -> int:
        self.ran.append(argv)
        return 0

    def want(self, *agents: dict[str, object]) -> None:
        self.api.document = {"revision": 2, "agents": list(agents)}
