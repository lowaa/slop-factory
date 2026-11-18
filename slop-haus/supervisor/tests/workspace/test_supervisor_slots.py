from __future__ import annotations

import json
import os
import stat
import subprocess
import unittest.mock
from pathlib import Path

from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace.update_script import UpdateScript
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.handover import GitSlotRepo
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.workspace.layout import Layout
from support import TempDirTestCase, make_archive

CONFIG = SupervisorConfig("https://slop-haus.example", "tenant-uuid", "acme")
SECRET = "slot-access-secret"
TOKENS = TokenPair(SECRET, "slot-refresh-secret", 1900)


class FakeService:
    """Returns what the test scripted and records the calls; it decides nothing."""

    def __init__(self) -> None:
        self.calls: list[str] = []
        self.script = "#!/usr/bin/env bash\n"
        self.fail_on: dict[str, Exception] = {}
        self.slots: dict[str, SlotRef] = {}
        self.script_fetched_with: list[TokenPair] = []
        self.retired_names: set[str] = set()

    def _step(self, call: str) -> None:
        self.calls.append(call)
        if call.split(" ")[0] in self.fail_on:
            raise self.fail_on[call.split(" ")[0]]

    def create_slot(self, name: str) -> SlotRef:
        self._step(f"create {name}")
        return self.slots.setdefault(name, SlotRef(f"id-{name}", name))

    def find_slot(self, name: str) -> SlotRef | None:
        self._step(f"find {name}")
        return self.slots.get(name)

    def adopt_slot(self, name: str) -> SlotRef:
        self._step(f"adopt {name}")
        return self.slots.setdefault(name, SlotRef(f"id-{name}", name))

    def is_retired(self, name: str) -> bool:
        self._step(f"is_retired {name}")
        return name in self.retired_names

    def retire_slot(self, slot: SlotRef) -> None:
        self._step(f"retire {slot.name}")
        self.slots.pop(slot.name, None)

    def issue_credential(self, slot: SlotRef) -> TokenPair:
        self._step(f"credential {slot.name}")
        return TOKENS

    def update_script(self, tokens: TokenPair) -> str:
        self._step("update_script")
        self.script_fetched_with.append(tokens)
        return self.script


class ProvisionedSlotTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "acme")
        lay.set_up(self.layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        self.service = FakeService()
        self.ran: list[tuple[list[str], Path]] = []

    def command(self, argv: list[str], cwd: Path, status: int = 0) -> int:
        self.ran.append((argv, cwd))
        return status

    def add(self, name: str = "w1", status: int = 0) -> SupervisorConfig:
        return lay.add_slot(self.layout, name, None, lambda argv, cwd: self.command(argv, cwd, status), self.service)

    def test_creates_the_agent_writes_its_credentials_and_installs_the_workspace(self) -> None:
        updated = self.add()

        slot = self.layout.slot("w1")
        self.assertEqual(("w1",), updated.slots)
        self.assertEqual(["create w1", "credential w1", "update_script"], self.service.calls)
        self.assertEqual([TOKENS], self.service.script_fetched_with)
        credentials = slot / ".slop-haus" / "credentials.json"
        self.assertEqual(SECRET, json.loads(credentials.read_text())["access_token"])
        self.assertEqual(0o600, stat.S_IMODE(credentials.stat().st_mode))
        self.assertEqual("SLOP_HAUS_AGENT=w1\n", (slot / ".slop-haus.env").read_text())
        self.assertEqual(1, len(self.ran))
        self.assertEqual("bash", self.ran[0][0][0])
        self.assertEqual(slot, self.ran[0][1])

    def test_a_clone_that_did_not_happen_fails_the_add_naming_the_repo(self) -> None:
        # The update script carries on past a failed clone; the add must not record such a slot.
        self.service.script = UpdateScript(
            '#!/usr/bin/env bash\n"$workspace"/.slop-haus/sync-repos.sh -- \'app\' \'git.example/acme/app\' || echo "not every repo" >&2\n',
            (("app", "git.example/acme/app"),),
        )

        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": "https"}):
            with self.assertRaisesRegex(cfg.ConfigError, "not a checkout of git.example/acme/app"):
                self.add()

        self.assertEqual((), cfg.load(self.layout.config_file).slots)
        self.assertIn("retire w1", self.service.calls)

    def test_a_checkout_of_another_repo_or_host_fails_the_add(self) -> None:
        self.service.script = UpdateScript(
            '#!/usr/bin/env bash\n"$workspace"/.slop-haus/sync-repos.sh -- \'app\' \'github.com/acme/app\'\n', (("app", "github.com/acme/app"),)
        )

        def clone_elsewhere(argv: list[str], cwd: Path) -> int:
            subprocess.run(["git", "init", "-q", str(cwd / "app")], check=True)
            subprocess.run(["git", "-C", str(cwd / "app"), "remote", "add", "origin", "https://evilgithub.com/acme/app.git"], check=True)
            return 0

        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": "https"}):
            with self.assertRaisesRegex(cfg.ConfigError, "not a checkout of github.com/acme/app"):
                lay.add_slot(self.layout, "w1", None, clone_elsewhere, self.service)

    def test_the_workspaces_no_clone_marker_skips_the_check(self) -> None:
        self.service.script = UpdateScript(
            '#!/usr/bin/env bash\n"$workspace"/.slop-haus/sync-repos.sh -- \'app\' \'git.example/acme/app\'\n', (("app", "git.example/acme/app"),)
        )

        def opt_out(argv: list[str], cwd: Path) -> int:
            (cwd / ".slop-haus").mkdir(exist_ok=True)
            (cwd / ".slop-haus" / "no-clone").write_text("")
            return 0

        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": "https"}):
            updated = lay.add_slot(self.layout, "w1", None, opt_out, self.service)

        self.assertEqual(("w1",), updated.slots)

    def test_cap_reached_leaves_no_directory_and_asks_for_nothing_else(self) -> None:
        self.service.fail_on["create"] = ApiError("the server would not create slot 'w1': at its slot cap", 409)

        with self.assertRaises(ApiError) as caught:
            self.add()

        self.assertIn("slot cap", str(caught.exception))
        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual(["create w1"], self.service.calls)
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

    def test_name_taken_is_the_servers_answer_and_changes_nothing_here(self) -> None:
        self.service.fail_on["create"] = ApiError("an agent with that name already exists", 409)

        with self.assertRaises(ApiError):
            self.add()

        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual([], self.ran)

    def test_a_failed_credential_fetch_retires_the_slot_and_leaves_no_half_slot(self) -> None:
        self.service.fail_on["credential"] = ApiError("503: slot credentials are not available", 503)

        with self.assertRaises(ApiError):
            self.add()

        self.assertEqual(["create w1", "credential w1", "retire w1"], self.service.calls)
        self.assertNotIn("w1", self.service.slots)
        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

    def test_a_failed_install_after_the_credential_is_written_still_rolls_back(self) -> None:
        with self.assertRaises(cfg.ConfigError):
            self.add(status=1)

        self.assertIn("retire w1", self.service.calls)
        self.assertNotIn("w1", self.service.slots)
        self.assertIn("bash", [argv[0] for argv, _ in self.ran])
        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

    def test_an_interrupted_add_rolls_back_and_the_same_name_can_be_added_again(self) -> None:
        def interrupted(argv: list[str], cwd: Path) -> int:
            raise KeyboardInterrupt

        with self.assertRaises(KeyboardInterrupt):
            lay.add_slot(self.layout, "w1", None, interrupted, self.service)
        self.assertIn("retire w1", self.service.calls)
        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

        self.assertEqual(("w1",), self.add().slots)

    def test_a_rollback_that_cannot_retire_says_how_to_finish(self) -> None:
        self.service.fail_on["update_script"] = ApiError("boom", 500)
        self.service.fail_on["retire"] = ApiError("down", 503)

        with self.assertRaises(ApiError) as caught:
            self.add()

        self.assertTrue(any("slot remove w1" in note for note in caught.exception.__notes__))
        self.assertFalse(self.layout.slot("w1").exists())

    def test_a_directory_that_was_there_before_is_kept_on_rollback(self) -> None:
        mine = self.layout.slot("w1")
        mine.mkdir(parents=True)
        (mine / "notes").write_text("mine")
        self.service.fail_on["credential"] = ApiError("no", 500)

        with self.assertRaises(ApiError):
            self.add()

        self.assertEqual("mine", (mine / "notes").read_text())

    def test_no_credential_value_reaches_the_commands_run_or_the_errors(self) -> None:
        with self.assertRaises(cfg.ConfigError) as caught:
            self.add(status=1)

        seen = json.dumps(self.ran, default=str) + str(caught.exception)
        self.assertNotIn(SECRET, seen)
        self.assertNotIn("slot-refresh-secret", seen)

    def test_an_existing_slot_is_refused_before_the_server_is_asked(self) -> None:
        self.add()
        self.service.calls.clear()

        with self.assertRaises(cfg.ConfigError):
            self.add()

        self.assertEqual([], self.service.calls)

    def test_without_a_service_the_setup_script_is_still_required(self) -> None:
        with self.assertRaises(cfg.ConfigError) as caught:
            lay.add_slot(self.layout, "w1", None, self.command)

        self.assertIn("login", str(caught.exception))


def git(cwd: Path, *args: str) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True)


class RemoveSlotTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "acme")
        lay.set_up(self.layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        self.service = FakeService()
        self.repo = GitSlotRepo(SubprocessRunner())
        # A slot cloned from a real origin, with everything pushed.
        origin = self.tmp / "origin"
        git(self.tmp, "init", "-q", "--bare", "-b", "main", str(origin))
        self.slot = self.layout.slot("w1")
        git(self.tmp, "clone", "-q", str(origin), str(self.slot))
        git(self.slot, "config", "user.email", "t@example.com")
        git(self.slot, "config", "user.name", "t")
        git(self.slot, "commit", "-q", "--allow-empty", "-m", "first")
        git(self.slot, "push", "-q", "origin", "main")
        self.service.slots["w1"] = SlotRef("id-w1", "w1")
        self.layout.config_file.write_text(cfg.load(self.layout.config_file).with_slot("w1").dumps())

    def test_retires_the_agent_deletes_the_directory_and_forgets_the_slot(self) -> None:
        updated = lay.remove_slot(self.layout, "w1", self.service, self.repo)

        self.assertEqual((), updated.slots)
        self.assertEqual(["find w1", "retire w1"], self.service.calls)
        self.assertFalse(self.slot.exists())
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

    def test_an_unpushed_commit_refuses_and_retires_nothing(self) -> None:
        git(self.slot, "commit", "-q", "--allow-empty", "-m", "local only")

        with self.assertRaises(cfg.ConfigError) as caught:
            lay.remove_slot(self.layout, "w1", self.service, self.repo)

        self.assertIn("not pushed", str(caught.exception))
        self.assertNotIn("retire w1", self.service.calls)
        self.assertTrue(self.slot.exists())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)

    def test_tracked_changes_refuse_too(self) -> None:
        (self.slot / "f").write_text("x")
        git(self.slot, "add", "f")
        git(self.slot, "commit", "-q", "-m", "f")
        git(self.slot, "push", "-q", "origin", "main")
        (self.slot / "f").write_text("changed")

        with self.assertRaises(cfg.ConfigError):
            lay.remove_slot(self.layout, "w1", self.service, self.repo)

        self.assertTrue(self.slot.exists())

    def test_a_commit_on_another_branch_counts(self) -> None:
        git(self.slot, "switch", "-q", "-c", "ticket-1-a1")
        git(self.slot, "commit", "-q", "--allow-empty", "-m", "wip")
        git(self.slot, "switch", "-q", "main")

        with self.assertRaises(cfg.ConfigError):
            lay.remove_slot(self.layout, "w1", self.service, self.repo)

    def test_a_retire_that_fails_keeps_the_directory(self) -> None:
        self.service.fail_on["retire"] = ApiError("down", 503)

        with self.assertRaises(ApiError):
            lay.remove_slot(self.layout, "w1", self.service, self.repo)

        self.assertTrue(self.slot.exists())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)

    def test_an_unknown_slot_is_an_error(self) -> None:
        with self.assertRaises(cfg.ConfigError):
            lay.remove_slot(self.layout, "nope", self.service, self.repo)

    def test_an_already_retired_slot_says_so(self) -> None:
        self.service.retired_names.add("gone")

        with self.assertRaises(cfg.ConfigError) as caught:
            lay.remove_slot(self.layout, "gone", self.service, self.repo)

        self.assertEqual("slot 'gone' is already retired: nothing is left to remove", str(caught.exception))
        self.assertNotIn("retire gone", self.service.calls)

    def test_an_unknown_slot_says_no_slot(self) -> None:
        with self.assertRaises(cfg.ConfigError) as caught:
            lay.remove_slot(self.layout, "nope", self.service, self.repo)

        self.assertEqual("no slot 'nope'", str(caught.exception))

    def test_a_leftover_agent_not_in_the_config_can_still_be_retired(self) -> None:
        self.layout.config_file.write_text(CONFIG.dumps())
        self.service.slots["w1"] = SlotRef("id-w1", "w1")

        lay.remove_slot(self.layout, "w1", self.service, self.repo)

        self.assertIn("retire w1", self.service.calls)

    def test_without_the_supervisor_signed_in_only_the_directory_goes(self) -> None:
        updated = lay.remove_slot(self.layout, "w1", None, self.repo)

        self.assertEqual((), updated.slots)
        self.assertFalse(self.slot.exists())
