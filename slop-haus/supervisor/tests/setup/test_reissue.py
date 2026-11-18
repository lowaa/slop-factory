from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.setup.reissue import reissue_slot_credentials
from slop_haus_supervisor.slophaus.credentials import CREDENTIALS, TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.config import ConfigError, SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout


class FakeService:
    def __init__(self, refuse: set[str] = frozenset()) -> None:  # pyright: ignore[reportArgumentType]
        self.refuse = refuse
        self.issued: list[str] = []

    def find_slot(self, name: str) -> SlotRef | None:
        return SlotRef(f"id-{name}", name)

    def issue_credential(self, slot: SlotRef) -> TokenPair:
        if slot.name in self.refuse:
            raise ConfigError("slot is disabled")
        self.issued.append(slot.name)
        return TokenPair(f"access-{slot.name}", f"refresh-{slot.name}", 4_000_000_000)


class ReissueTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        config = SupervisorConfig("https://s.example", "tid", "acme", "claude", "")
        cfg.write(self.layout.config_file, config.with_slot("a").with_slot("b"))
        self.said: list[str] = []

    def stored(self, name: str) -> dict[str, object]:
        return json.loads((self.layout.slot(name) / ".slop-haus" / CREDENTIALS).read_text())

    def test_every_slot_gets_the_new_credential_written(self) -> None:
        failed = reissue_slot_credentials(self.layout, FakeService(), None, self.said.append)  # pyright: ignore[reportArgumentType]

        self.assertEqual(0, failed)
        self.assertIn("access-a", json.dumps(self.stored("a")))
        self.assertIn("refresh-b", json.dumps(self.stored("b")))

    def test_a_refused_slot_is_named_and_the_others_still_get_theirs(self) -> None:
        failed = reissue_slot_credentials(self.layout, FakeService({"a"}), None, self.said.append)  # pyright: ignore[reportArgumentType]

        self.assertEqual(1, failed)
        self.assertIn("slot a: credential not reissued: slot is disabled", self.said)
        self.assertIn("refresh-b", json.dumps(self.stored("b")))

    def test_one_named_slot_leaves_the_rest_alone(self) -> None:
        service = FakeService()

        reissue_slot_credentials(self.layout, service, ["b"], self.said.append)  # pyright: ignore[reportArgumentType]

        self.assertEqual(["b"], service.issued)

    def test_an_unknown_slot_is_refused(self) -> None:
        with self.assertRaises(ConfigError):
            reissue_slot_credentials(self.layout, FakeService(), ["zz"], self.said.append)  # pyright: ignore[reportArgumentType]
