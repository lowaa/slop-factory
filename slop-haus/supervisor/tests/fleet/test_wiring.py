from __future__ import annotations

import logging
import os
import threading
import unittest
from typing import Any
from unittest import mock
from pathlib import Path
import tempfile

from slop_haus_supervisor.fleet import wiring
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.pi import PiHarness
from slop_haus_supervisor.harness.denied import DENIED_TOOLS
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.briefing import SlotBriefing
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.harness_config import HarnessConfig, HarnessConfigStore
from slop_haus_supervisor.workspace.layout import Layout
from slop_haus_supervisor.workspace.update_script import UpdateScript


def _variant(deps: Any, agent: str) -> str:
    assert deps.variant_of is not None
    return deps.variant_of(agent)


class WiringTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))

    def build(self, config: SupervisorConfig) -> None:
        self.build_scheduler(config)

    def build_scheduler(self, config: SupervisorConfig):
        return wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())

    def test_read_secrets_skips_placeholders_and_comments(self) -> None:
        self.layout.secrets_file.write_text("# note\nCLAUDE_CODE_OAUTH_TOKEN=\nANTHROPIC_API_KEY=k\n")

        self.assertEqual({"ANTHROPIC_API_KEY": "k"}, wiring.read_secrets(self.layout.secrets_file))

    def test_no_slots_is_refused(self) -> None:
        with self.assertRaisesRegex(cfg.ConfigError, "no slots"):
            self.build(SupervisorConfig("https://s", "t", "acme"))

    def test_a_missing_credential_is_refused(self) -> None:
        self.layout.secrets_file.write_text("CLAUDE_CODE_OAUTH_TOKEN=\n")

        with self.assertRaisesRegex(cfg.ConfigError, "CLAUDE_CODE_OAUTH_TOKEN"):
            self.build(SupervisorConfig("https://s", "t", "acme", slots=("a",)))

    def test_the_built_scheduler_reports_slot_state(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        config = SupervisorConfig("https://s", "t", "acme", slots=("a",))

        with mock.patch.object(ClaudeHarness, "check_installed"):
            scheduler = wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())

        self.assertIsNotNone(scheduler.reporter)

    def test_pi_without_its_extension_is_refused(self) -> None:
        self.layout.secrets_file.write_text("OPENAI_API_KEY=k\n")

        with self.assertRaisesRegex(cfg.ConfigError, "extension"):
            self.build(SupervisorConfig("https://s", "t", "acme", harness="pi", slots=("a",)))

    def test_pi_without_a_provider_key_is_refused(self) -> None:
        self.layout.secrets_file.write_text("")
        extension = self.layout.root / ".slop-haus" / "pi" / "slop-haus.ts"
        extension.parent.mkdir(parents=True)
        extension.write_text("")

        with self.assertRaisesRegex(cfg.ConfigError, "provider .*_API_KEY"):
            self.build(SupervisorConfig("https://s", "t", "acme", harness="pi", slots=("a",)))

    def test_pi_is_built_when_configured(self) -> None:
        self.layout.secrets_file.write_text("OPENAI_API_KEY=k\n")
        extension = self.layout.root / ".slop-haus" / "pi" / "slop-haus.ts"
        extension.parent.mkdir(parents=True)
        extension.write_text("")

        with mock.patch.object(PiHarness, "check_installed"):
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme", harness="pi", slots=("a",)))

        self.assertIsNotNone(scheduler.reporter)

    def test_a_pi_slot_carries_the_container_or_vm_warning_and_a_claude_slot_does_not(self) -> None:
        self.layout.secrets_file.write_text("OPENAI_API_KEY=k\nANTHROPIC_API_KEY=k\n")
        extension = self.layout.root / ".slop-haus" / "pi" / "slop-haus.ts"
        extension.parent.mkdir(parents=True)
        extension.write_text("")

        with mock.patch.object(PiHarness, "check_installed"), self.assertLogs("test", "WARNING") as logged:
            self.build(SupervisorConfig("https://s", "t", "acme", harness="pi", slots=("a",)))
        self.assertIn("container or VM", "\n".join(logged.output))

        with mock.patch.object(ClaudeHarness, "check_installed"), self.assertNoLogs("test", "WARNING"):
            self.build(SupervisorConfig("https://s", "t", "acme", slots=("a",)))

    def test_an_unknown_harness_is_refused(self) -> None:
        with self.assertRaisesRegex(cfg.ConfigError, "unknown harness"):
            self.build(SupervisorConfig("https://s", "t", "acme", harness="gpt", slots=("a",)))

    def test_runs_deny_what_the_servers_headless_list_denies(self) -> None:
        self.assertEqual(
            {"mcp__slop-haus__ticket_next", "Monitor"},
            set(DENIED_TOOLS),
        )

    def test_built_slots_brief_runs_from_their_own_files(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        config = SupervisorConfig("https://s", "t", "acme", slots=("a",))
        with mock.patch.object(ClaudeHarness, "check_installed"), mock.patch.object(wiring, "Slot") as slot:
            wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())

        deps = slot.call_args.args[2]
        self.assertIsInstance(deps.briefing, SlotBriefing)

    def test_no_steward_until_the_supervisor_has_its_own_credential(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        config = SupervisorConfig("https://s", "t", "acme", slots=("a",))
        with mock.patch.object(ClaudeHarness, "check_installed"):
            scheduler = wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())
        self.assertIsNone(scheduler._steward)  # pyright: ignore[reportPrivateUsage]

    def test_the_steward_is_built_once_the_supervisor_has_signed_in(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        (self.layout.supervisor_dir / ".slop-haus").mkdir(parents=True)
        (self.layout.supervisor_dir / ".slop-haus" / "credentials.json").write_text("{}")
        config = SupervisorConfig("https://s", "t", "acme", slots=("a",))
        with mock.patch.object(ClaudeHarness, "check_installed"):
            scheduler = wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())
        self.assertIsInstance(scheduler._steward, wiring.StewardRunner)  # pyright: ignore[reportPrivateUsage]

    def test_a_codex_steward_reviews_the_slot_checkouts_and_may_not_read_the_workspace(self) -> None:
        for name in ("b", "a"):
            self.layout.slot(name).mkdir(parents=True)
        (self.layout.slots / "stray-file").write_text("")
        self.assertEqual([self.layout.slot("a"), self.layout.slot("b")], wiring.slot_checkouts(self.layout))
        self.assertEqual(
            [self.layout.root, self.layout.supervisor_dir, self.layout.state_dir], wiring.steward_forbidden(self.layout)
        )

    def sign_in(self) -> None:
        (self.layout.supervisor_dir / ".slop-haus").mkdir(parents=True)
        (self.layout.supervisor_dir / ".slop-haus" / "credentials.json").write_text("{}")

    def test_a_reconciler_is_built_only_when_signed_in(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        config = SupervisorConfig("https://s", "t", "acme", slots=("a",))
        with mock.patch.object(ClaudeHarness, "check_installed"):
            plain = self.build_scheduler(config)
            self.sign_in()
            signed = self.build_scheduler(config)
        self.assertIsNone(plain._reconciler)  # pyright: ignore[reportPrivateUsage]
        self.assertIsNotNone(signed._reconciler)  # pyright: ignore[reportPrivateUsage]
        self.assertIsNotNone(signed._reconciler.target)  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownArgumentType, reportUnknownMemberType]

    def test_the_scheduler_ticks_the_reconciler(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with mock.patch.object(ClaudeHarness, "check_installed"), mock.patch.object(wiring.Reconciler, "tick") as tick:
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            scheduler._reconciler.tick()  # pyright: ignore[reportOptionalMemberAccess, reportPrivateUsage]
        tick.assert_called_once_with()

    def test_zero_slots_are_accepted_when_signed_in(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with mock.patch.object(ClaudeHarness, "check_installed"):
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
        self.assertEqual(set(), scheduler.slot_names())

    def test_a_board_added_slot_gets_its_token_and_repo_env(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.layout.repo_env_file.write_text("KEY=value1\n")
        self.sign_in()
        slot_dir = self.layout.slot("new")
        (slot_dir / ".git").mkdir(parents=True)
        with mock.patch.object(ClaudeHarness, "check_installed"), mock.patch.object(wiring, "HttpSlopHaus") as client:
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            slot = host._make_slot("new", "claude")  # pyright: ignore[reportUnknownMemberType, reportUnknownVariableType]

        self.assertEqual("new", slot.agent)  # pyright: ignore[reportUnknownMemberType]
        self.assertIn("new", client.call_args.args[1])
        self.assertEqual("KEY=value1\n", (slot_dir / ".env").read_text())

    def test_a_board_added_remote_control_host_is_built_on_its_own_workspace(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with mock.patch.object(ClaudeHarness, "check_installed"), mock.patch.object(wiring, "HttpSlopHaus") as client:
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            # Stepping it is the slot's own tests': here only that the scheduler holds the right slot.
            with mock.patch.object(scheduler, "_start"):
                host.start_host("rc1", "claude", False)  # pyright: ignore[reportUnknownMemberType]
            with self.assertRaises(cfg.ConfigError):
                host.start_host("rc2", "emacs", False)  # pyright: ignore[reportUnknownMemberType]

        self.assertEqual({"rc1"}, scheduler.slot_names())
        slot = scheduler._entries["rc1"].slot  # pyright: ignore[reportPrivateUsage]
        self.assertEqual(self.layout.remote_control_workspace("rc1"), slot._d.workspace._unit.directory)  # pyright: ignore[reportAttributeAccessIssue, reportUnknownMemberType]
        # It clones as this supervisor's slots do: its protocol is chosen from this layout's supervisor.toml.
        self.layout.config_file.write_text(SupervisorConfig("https://s", "t", "acme").with_clone_protocol("ssh").dumps())
        with mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""}):
            chosen = slot._d.workspace._protocol_for(UpdateScript("", (("app", "git.example/acme/app"),)))  # pyright: ignore[reportAttributeAccessIssue, reportUnknownMemberType, reportUnknownVariableType]
        self.assertEqual("ssh", chosen)
        self.assertEqual("claude", slot.harness)  # pyright: ignore[reportAttributeAccessIssue, reportUnknownMemberType]
        # The scheduler reports its slot state as rc1: the client must hold rc1's own token helper.
        tokens = client.call_args.args[1]
        self.assertEqual(self.layout.remote_control_workspace("rc1") / ".slop-haus" / "token.sh", tokens["rc1"]._helper)

    def test_hosts_are_codex_users_whatever_the_workers_harness(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with mock.patch.object(ClaudeHarness, "check_installed"), mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("SLOP_HAUS_CODEX_SHARED_LOGIN", None)
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            with mock.patch.object(scheduler, "_start"):
                host.start_host("rc1", "codex", False)  # pyright: ignore[reportUnknownMemberType]
                with self.assertRaises(cfg.ConfigError):
                    host.start_host("rc2", "codex", False)  # pyright: ignore[reportUnknownMemberType]

    def test_each_slot_runs_the_harness_it_was_made_with(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\nOPENAI_API_KEY=k\n")
        self.sign_in()
        extension = self.layout.root / ".slop-haus" / "pi" / "slop-haus.ts"
        extension.parent.mkdir(parents=True)
        extension.write_text("")
        with (
            mock.patch.object(ClaudeHarness, "check_installed"),
            mock.patch.object(PiHarness, "check_installed"),
            mock.patch.object(wiring, "HttpSlopHaus") as client,
        ):
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host: Any = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]

            with mock.patch.object(scheduler, "_start"):
                for name, harness in (("a", "claude"), ("b", "pi"), ("c", "claude")):
                    host.start_slot(name, harness)  # pyright: ignore[reportUnknownMemberType]
            ran = {name: scheduler._entries[name].slot._d.harness.name for name in ("a", "b", "c")}  # pyright: ignore[reportAttributeAccessIssue, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            self.assertEqual({"a": "claude", "b": "pi", "c": "claude"}, ran)
            self.assertEqual("pi", host.harness_of("b"))  # pyright: ignore[reportUnknownMemberType]
        # One client per harness, which tells the server which harness claims.
        self.assertEqual(["claude", "pi"], [call.args[2] for call in client.call_args_list])

    def test_a_slot_on_a_harness_that_is_not_ready_is_refused(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with mock.patch.object(ClaudeHarness, "check_installed"):
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            with self.assertRaises(cfg.ConfigError):
                host._make_slot("a", "pi")  # pyright: ignore[reportUnknownMemberType]
            with self.assertRaises(cfg.ConfigError):
                host._make_slot("a", "emacs")  # pyright: ignore[reportUnknownMemberType]
            with mock.patch.object(CodexHarness, "missing_credentials", return_value="a ChatGPT login"):
                with self.assertRaisesRegex(cfg.ConfigError, "ChatGPT login"):
                    host._make_slot("a", "codex")  # pyright: ignore[reportUnknownMemberType]
            with (
                mock.patch.object(CodexHarness, "missing_credentials", return_value=""),
                mock.patch.object(CodexHarness, "check_installed", side_effect=FileNotFoundError("codex is not installed")),
            ):
                with self.assertRaisesRegex(cfg.ConfigError, "not installed"):
                    host._make_slot("a", "codex")  # pyright: ignore[reportUnknownMemberType]

    def test_the_codex_guard_counts_only_codex_slots(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        with (
            mock.patch.object(ClaudeHarness, "check_installed"),
            mock.patch.object(CodexHarness, "check_installed"),
            mock.patch.object(CodexHarness, "missing_credentials", return_value=""),
            mock.patch.object(wiring, "HttpSlopHaus"),
            mock.patch.dict(os.environ, {}, clear=False),
        ):
            os.environ.pop("SLOP_HAUS_CODEX_SHARED_LOGIN", None)
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme"))
            host = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            with mock.patch.object(scheduler, "_start"):
                for name in ("c1", "c2", "c3"):
                    host.start_slot(name, "claude")  # pyright: ignore[reportUnknownMemberType]
                host.start_slot("x1", "codex")  # pyright: ignore[reportUnknownMemberType]
                with self.assertRaisesRegex(cfg.ConfigError, "Codex slot"):
                    host.start_slot("x2", "codex")  # pyright: ignore[reportUnknownMemberType]

    def test_a_slot_takes_its_variant_from_the_board_config_of_its_own_harness(self) -> None:
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=k\n")
        self.sign_in()
        board = HarnessConfig(1, "codex", {"codex": {"model": "gpt-5", "effort": "high"}, "claude": {"model": "opus"}})
        with (
            mock.patch.object(ClaudeHarness, "check_installed"),
            mock.patch.object(CodexHarness, "check_installed"),
            mock.patch.object(CodexHarness, "missing_credentials", return_value=""),
            mock.patch.object(wiring, "HttpSlopHaus"),
            mock.patch.object(HarnessConfigStore, "refresh", return_value=board),
        ):
            scheduler = self.build_scheduler(SupervisorConfig("https://s", "t", "acme", variant="model=toml"))
            host: Any = scheduler._reconciler.target._host  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess, reportPrivateUsage, reportUnknownMemberType, reportUnknownVariableType]
            codex = host._make_slot("x", "codex")._d  # pyright: ignore[reportUnknownMemberType, reportUnknownVariableType]
            claude = host._make_slot("c", "claude")._d  # pyright: ignore[reportUnknownMemberType, reportUnknownVariableType]

            self.assertEqual("model=gpt-5,effort=high", _variant(codex, "x"))
            self.assertEqual("model=opus", _variant(claude, "c"))
        # Settings for another harness only: Codex keeps its own defaults, and the toml variant is not Codex's.
        other = HarnessConfig(1, "", {"claude": {"model": "opus"}})
        with mock.patch.object(HarnessConfigStore, "refresh", return_value=other):
            self.assertEqual("", _variant(codex, "x"))
        with mock.patch.object(HarnessConfigStore, "refresh", return_value=None):
            # supervisor.toml's variant is written for the default harness only.
            self.assertEqual("", _variant(codex, "x"))
            self.assertEqual("model=toml", _variant(claude, "c"))

    def test_the_old_stub_prompt_is_gone(self) -> None:
        self.assertFalse(hasattr(wiring, "HEADLESS_PROMPT"))


class CodexSlotGuardWiringTest(unittest.TestCase):
    """Codex is not yet registered as a selectable harness, so the factory is replaced by one that returns it."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))

    def build(self, config: SupervisorConfig) -> None:
        wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())

    def build_codex(self, environ: dict[str, str]) -> None:
        config = SupervisorConfig("https://s", "t", "acme", harness="claude", slots=("a", "b"))
        codex = CodexHarness(self.layout.root, "https://s")
        with (
            mock.patch.object(wiring, "build_harness", return_value=codex),
            mock.patch.dict("os.environ", environ),
            mock.patch.object(CodexHarness, "check_installed"),
        ):
            self.build(config)

    def test_the_flag_gives_the_codex_harness_a_shared_login(self) -> None:
        contexts: list[Any] = []
        config = SupervisorConfig("https://s", "t", "acme", harness="claude", slots=("a", "b"))

        def capture(_kind: Any, _name: str, context: Any, **_kw: Any) -> CodexHarness:
            contexts.append(context)
            return CodexHarness(self.layout.root, "https://s", login=context.codex_login)

        for flag, expected in (("verified", True), ("", False)):
            contexts.clear()
            with (
                mock.patch.object(wiring, "build_harness", side_effect=capture),
                mock.patch.dict("os.environ", {"SLOP_HAUS_CODEX_SHARED_LOGIN": flag}),
                mock.patch.object(CodexHarness, "check_installed"),
                self.assertRaises(cfg.ConfigError),
            ):
                self.build(config)
            self.assertEqual(expected, contexts[0].codex_login is not None)

    def test_two_codex_slots_are_refused_without_the_owners_flag(self) -> None:
        with mock.patch.dict("os.environ"):
            os.environ.pop("SLOP_HAUS_CODEX_SHARED_LOGIN", None)
            with self.assertRaisesRegex(cfg.ConfigError, "SLOP_HAUS_CODEX_SHARED_LOGIN=verified"):
                self.build_codex({})

    def test_the_flag_lets_two_codex_slots_through_to_the_credential_check(self) -> None:
        with self.assertRaisesRegex(cfg.ConfigError, "ChatGPT login"):
            self.build_codex({"SLOP_HAUS_CODEX_SHARED_LOGIN": "verified"})
