from __future__ import annotations

import logging
import threading
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.fleet import wiring
from slop_haus_supervisor.harness import factory
from slop_haus_supervisor.harness.base import Usage
from slop_haus_supervisor.slophaus.client import SlotSnapshot
from slop_haus_supervisor.slophaus.http_client import HttpSlopHaus
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout
from test_http_client import Stub, serve


class SecretsStayLocalTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)

    def test_no_secret_value_or_key_name_is_sent_by_the_client(self) -> None:
        stub = Stub()
        server = serve(stub)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        layout = Layout(self.tmp)
        layout.secrets_file.write_text("ANTHROPIC_API_KEY=value-one-aaa\nWORKER_SPECIAL_KEY=value-two-bbb\n")
        layout.repo_env_file.write_text("SELF_REVIEW_KEY=value-three-ccc\n")
        slot = layout.slot("a")
        (slot / ".slop-haus").mkdir(parents=True)
        helper = slot / ".slop-haus" / "token.sh"
        helper.write_text("#!/bin/sh\necho tok\n")
        helper.chmod(0o755)
        config = SupervisorConfig(f"http://127.0.0.1:{server.server_address[1]}", "t", "acme", slots=("a",))

        with (
            mock.patch.object(factory.ClaudeHarness, "check_installed"),
            mock.patch.object(wiring, "Slot") as slot_cls,
            mock.patch.object(factory, "ClaudeHarness", wraps=factory.ClaudeHarness) as harness_cls,
        ):
            wiring.build(layout, config, logging.getLogger("test"), threading.Event())
        kwargs = harness_cls.call_args.kwargs
        self.assertEqual({"ANTHROPIC_API_KEY": "value-one-aaa", "WORKER_SPECIAL_KEY": "value-two-bbb"}, kwargs["credentials"])
        masked = kwargs["masker"].mask("value-one-aaa value-two-bbb value-three-ccc")
        self.assertEqual("*** *** ***", masked)
        client = slot_cls.call_args.args[2].client
        assert isinstance(client, HttpSlopHaus)
        client.heartbeat("a")
        client.report_slot_state("a", SlotSnapshot("idle", 1.0))
        client.report_usage("a", Usage())
        client.state("a")

        self.assertTrue(stub.seen)
        sent = repr([(s.method, s.path, s.body) for s in stub.seen])
        for needle in ["value-one-aaa", "value-two-bbb", "value-three-ccc", "ANTHROPIC_API_KEY", "WORKER_SPECIAL_KEY", "SELF_REVIEW_KEY"]:
            self.assertNotIn(needle, sent)

    def test_repo_env_becomes_each_slots_private_dot_env(self) -> None:
        layout = Layout(self.tmp)
        layout.repo_env_file.write_text("SELF_REVIEW_KEY=abc\n")
        slots = [self.tmp / "s1", self.tmp / "s2"]
        for s in slots:
            (s / ".git").mkdir(parents=True)

        values = wiring.install_repo_env(layout, slots)

        self.assertEqual({"abc"}, values)
        for s in slots:
            self.assertEqual("SELF_REVIEW_KEY=abc\n", (s / ".env").read_text())
            self.assertEqual(0o600, (s / ".env").stat().st_mode & 0o777)

    def test_a_nested_slot_gets_the_dot_env_in_its_clone_not_its_root(self) -> None:
        layout = Layout(self.tmp)
        layout.repo_env_file.write_text("SELF_REVIEW_KEY=abc\n")
        slot = self.tmp / "s1"
        (slot / "app" / ".git").mkdir(parents=True)
        (slot / "docs").mkdir()

        wiring.install_repo_env(layout, [slot])

        self.assertEqual("SELF_REVIEW_KEY=abc\n", (slot / "app" / ".env").read_text())
        self.assertFalse((slot / ".env").exists())
        self.assertFalse((slot / "docs" / ".env").exists())

    def test_without_a_repo_env_a_slots_own_dot_env_is_left_alone(self) -> None:
        layout = Layout(self.tmp)
        (self.tmp / "s1").mkdir()
        (self.tmp / "s1" / ".env").write_text("MINE=1\n")

        self.assertEqual(set(), wiring.install_repo_env(layout, [self.tmp / "s1"]))
        self.assertEqual("MINE=1\n", (self.tmp / "s1" / ".env").read_text())

    def test_read_secrets_understands_export_and_quotes(self) -> None:
        path = self.tmp / "secrets.env"
        path.write_text("export A='x y'\nB=\"z\"\nC=\n")

        self.assertEqual({"A": "x y", "B": "z"}, wiring.read_secrets(path))


if __name__ == "__main__":
    unittest.main()
