from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.setup.guided import GuidedSetup, SetupOptions
from slop_haus_supervisor.setup.prerequisites import Finding
from slop_haus_supervisor.workspace.envfile import EnvFile
from slop_haus_supervisor.workspace.layout import Layout

TOKEN = "sk-ant-oat01-SUPERSECRETTOKEN"


class FakeHost:
    """The host's state across runs, so a re-run sees what an interrupted one left."""

    def __init__(self) -> None:
        self.findings = [Finding("uv", True, "found")]
        self.is_signed_in = False
        self.slots = 0
        self.logins = 0
        self.reissues = 0
        self.reissue_failures = 0
        self.interrupt_at = ""
        self.logged_in: set[str] = set()
        self.harness_logins: list[str] = []
        self.board_url: str | None = None
        self.path_additions: list[str] = []

    def prerequisites(self) -> list[Finding]:
        return self.findings

    def add_run_env_path(self, folder: str) -> None:
        self.path_additions.append(folder)
        self.findings = [f for f in self.findings if f.path_fix != folder]

    def signed_in(self) -> bool:
        return self.is_signed_in

    def login(self, slots: int) -> None:
        self._maybe_interrupt("login")
        self.logins += 1
        self.is_signed_in = True

    def reissue_slot_credentials(self) -> int:
        self.reissues += 1
        return self.reissue_failures

    def existing_slots(self) -> int:
        return self.slots

    def add_slots(self, count: int) -> None:
        for _ in range(count):
            self._maybe_interrupt("slot")
            self.slots += 1

    def board_workers_url(self) -> str | None:
        return self.board_url

    def harness_logged_in(self, name: str) -> bool:
        return name in self.logged_in

    def harness_login(self, name: str) -> list[str]:
        self.harness_logins.append(name)
        self.logged_in.add(name)
        return [f"{name} is logged in", "slots paused on the login: a"]

    def _maybe_interrupt(self, step: str) -> None:
        if self.interrupt_at == step:
            self.interrupt_at = ""
            raise KeyboardInterrupt


class FakePrompts:
    def __init__(self, slots: str = "", token: str = TOKEN, woodpecker: str = "") -> None:
        self.slots, self.hidden = slots, {"harness": token, "WOODPECKER": woodpecker}
        self.asked: list[str] = []

    def ask(self, question: str, default: str) -> str:
        self.asked.append(question)
        return self.slots or default

    def ask_hidden(self, question: str) -> str:
        self.asked.append(question)
        return next((value for key, value in self.hidden.items() if key in question), "")


class GuidedSetupTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.host = FakeHost()
        self.out: list[str] = []

    def run_setup(self, prompts: FakePrompts | None = None, slots: int | None = None) -> int:
        return GuidedSetup(self.layout, self.host, prompts or FakePrompts(), self.out.append).run(SetupOptions(slots))

    def test_a_sign_in_that_replaces_a_revoked_one_reissues_the_existing_slots_credentials(self) -> None:
        self.host.slots = 2

        self.assertEqual(0, self.run_setup(slots=2))

        self.assertEqual((1, 1), (self.host.logins, self.host.reissues))
        self.assertIn("[done] slot credentials", "\n".join(self.out))

    def test_reissue_failures_are_reported_not_hidden(self) -> None:
        self.host.slots = 2
        self.host.reissue_failures = 1

        self.assertEqual(0, self.run_setup(slots=2))

        self.assertIn("1 not reissued", "\n".join(self.out))

    def test_a_fresh_host_has_no_slot_credentials_to_reissue(self) -> None:
        self.run_setup()

        self.assertEqual(0, self.host.reissues)

    def test_a_valid_sign_in_is_left_alone(self) -> None:
        self.host.is_signed_in = True
        self.host.slots = 3

        self.run_setup()

        self.assertEqual((0, 0), (self.host.logins, self.host.reissues))

    def test_a_fresh_host_runs_every_step(self) -> None:
        self.assertEqual(0, self.run_setup())

        self.assertEqual((True, 3), (self.host.is_signed_in, self.host.slots))
        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertIn("slop-haus start", "\n".join(self.out))

    def test_a_board_provisioned_tenant_adds_no_slots_and_setup_points_at_the_board(self) -> None:
        self.host.board_url = "https://acme.example/workers"

        self.assertEqual(0, self.run_setup())

        text = "\n".join(self.out)
        self.assertEqual(0, self.host.slots)
        self.assertIn("[skipped] slots: this tenant provisions agents from the board", text)
        self.assertIn("https://acme.example/workers", text)
        self.assertIn("not managed by the board", text)
        self.assertIn("Setup is complete", text)

    def test_a_host_adding_its_own_slots_is_not_pointed_at_the_board(self) -> None:
        self.run_setup()

        self.assertNotIn("provisions agents from the board", "\n".join(self.out))

    def test_how_many_slots_is_asked_and_honoured(self) -> None:
        self.run_setup(FakePrompts(slots="2"))

        self.assertEqual(2, self.host.slots)

    def test_a_second_run_changes_nothing_and_asks_for_nothing_it_has(self) -> None:
        self.run_setup()
        prompts = FakePrompts()

        self.run_setup(prompts)

        self.assertEqual((1, 3), (self.host.logins, self.host.slots))
        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertIn("[skipped] sign-in: the supervisor is already signed in", self.out)

    def test_a_stored_token_kept_at_the_prompt_names_how_to_replace_it(self) -> None:
        self.run_setup()
        self.out.clear()

        self.run_setup(FakePrompts(token=""))

        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])
        skipped = next(line for line in self.out if "harness login token" in line)
        self.assertIn("slop-haus secret set CLAUDE_CODE_OAUTH_TOKEN", skipped)

    def test_a_stored_token_is_replaced_by_one_pasted_at_the_prompt(self) -> None:
        self.run_setup()
        new = "sk-ant-oat01-REPLACEMENTTOKEN"

        self.run_setup(FakePrompts(token=new))

        self.assertEqual(new, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertTrue(any("replaced" in line for line in self.out))

    def test_a_stored_api_key_is_replaced_under_its_own_name(self) -> None:
        EnvFile(self.layout.secrets_file).set("ANTHROPIC_API_KEY", "sk-ant-api03-OLDKEYVALUE")
        new = "sk-ant-api03-NEWKEYVALUE"

        self.run_setup(FakePrompts(token=new))

        values = EnvFile(self.layout.secrets_file).values()
        self.assertEqual(new, values["ANTHROPIC_API_KEY"])
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", values)

    def test_a_too_short_replacement_leaves_the_stored_token(self) -> None:
        self.run_setup()

        self.run_setup(FakePrompts(token="x"))

        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])

    def test_a_rerun_after_an_interruption_finishes_without_duplicating(self) -> None:
        for step in ("login", "slot"):
            host = self.host = FakeHost()
            host.interrupt_at = step
            with self.assertRaises(KeyboardInterrupt):
                self.run_setup()

            self.run_setup()

            self.assertEqual(3, host.slots, step)
            self.assertEqual(1, host.logins, step)

    def test_a_rerun_after_slots_were_half_added_adds_only_the_rest(self) -> None:
        self.host.interrupt_at = "slot"
        with self.assertRaises(KeyboardInterrupt):
            self.run_setup(slots=3)
        self.host.slots = 1

        self.run_setup(slots=3)

        self.assertEqual(3, self.host.slots)

    def test_a_missing_prerequisite_stops_before_anything_is_changed_and_names_the_fix(self) -> None:
        self.host.findings = [Finding("docker", False, "start the daemon")]

        self.assertEqual(1, self.run_setup())

        self.assertEqual((False, 0), (self.host.is_signed_in, self.host.slots))
        self.assertIn("[MISSING] docker: start the daemon", self.out)
        self.assertFalse(self.layout.secrets_file.exists())

    def test_a_warning_does_not_stop_setup(self) -> None:
        self.host.findings = [Finding("disk", False, "only 10 GB free", blocking=False)]

        self.assertEqual(0, self.run_setup())

        self.assertIn("[warning] disk: only 10 GB free", self.out)

    def test_an_optional_secret_is_stored_by_name(self) -> None:
        self.run_setup(FakePrompts(woodpecker="wp-token-value"))

        self.assertEqual("wp-token-value", EnvFile(self.layout.secrets_file).values()["WOODPECKER_TOKEN"])

    def test_a_pi_host_is_not_asked_for_a_claude_token(self) -> None:
        prompts = FakePrompts()

        GuidedSetup(self.layout, self.host, prompts, self.out.append, harness="pi").run(SetupOptions(None))

        self.assertFalse(any("harness login token" in q for q in prompts.asked))
        self.assertFalse(self.layout.secrets_file.exists())
        self.assertEqual(["pi"], self.host.harness_logins)
        self.assertIn("[done] harness login: pi is logged in", self.out)

    def test_a_logged_in_own_login_harness_is_not_logged_in_again(self) -> None:
        self.host.logged_in.add("pi")

        GuidedSetup(self.layout, self.host, FakePrompts(), self.out.append, harness="pi").run(SetupOptions(None))

        self.assertEqual([], self.host.harness_logins)
        self.assertIn("[skipped] harness login: pi is already logged in", self.out)

    def test_a_too_short_token_is_not_stored_or_echoed(self) -> None:
        self.run_setup(FakePrompts(token="q9z"))

        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", EnvFile(self.layout.secrets_file).values())
        self.assertNotIn("q9z", "\n".join(self.out))

    def test_secrets_never_appear_in_the_output(self) -> None:
        self.run_setup(FakePrompts(woodpecker="wp-token-value"))

        text = "\n".join(self.out)
        self.assertNotIn(TOKEN, text)
        self.assertNotIn("wp-token-value", text)

    def hidden_tool(self) -> None:
        self.host.findings = [Finding("claude", False, "is at /h/.local/bin/claude, which the service won't see", path_fix="/h/.local/bin")]

    def test_a_tool_only_the_shell_finds_is_fixed_when_the_person_agrees(self) -> None:
        self.hidden_tool()

        self.assertEqual(0, self.run_setup())

        self.assertEqual(["/h/.local/bin"], self.host.path_additions)
        self.assertIn("[done] run.env PATH", "\n".join(self.out))

    def test_two_tools_in_one_hidden_folder_are_one_offer(self) -> None:
        self.host.findings = [
            Finding("claude", False, "hidden", path_fix="/h/bin"),
            Finding("pi", False, "hidden", blocking=False, path_fix="/h/bin"),
        ]

        self.assertEqual(0, self.run_setup())

        self.assertEqual(["/h/bin"], self.host.path_additions)

    def test_declining_leaves_setup_stopped_on_the_tool(self) -> None:
        self.hidden_tool()

        self.assertEqual(1, self.run_setup(FakePrompts(slots="n")))

        self.assertEqual([], self.host.path_additions)
        self.assertIn("fix claude", "\n".join(self.out))

    def test_an_optional_tool_only_the_shell_finds_is_offered_too_and_setup_goes_on_when_declined(self) -> None:
        self.host.findings = [Finding("gh", False, "is at /h/bin/gh", blocking=False, path_fix="/h/bin")]

        prompts = FakePrompts(slots="n")

        self.assertEqual(0, self.run_setup(prompts))

        self.assertEqual([], self.host.path_additions)
        self.assertTrue(any("gh at /h/bin" in q for q in prompts.asked))
