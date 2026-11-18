from __future__ import annotations

import contextlib
import io
import stat
import sys
import unittest.mock

from slop_haus_supervisor import cli
from slop_haus_supervisor.workspace import envfile
from support import TempDirTestCase, make_archive

SENTINELS = ["sentinel-base-0001", "sentinel-kind-0002", "sentinel-agent-0003"]


class LayeredCliTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.cmd("init", "--server", "https://s.example/", "--tenant-id", "tid", "--tenant", "acme", "--package", str(make_archive(self.tmp / "p.tar.gz")), tenant=False)
        self.root = self.home / "slop-haus" / "acme"

    def cmd(self, *argv: str, stdin: str = "", tenant: bool = True) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), unittest.mock.patch.object(sys, "stdin", io.StringIO(stdin)):
            code = cli.main([*argv, *(["--tenant", "acme"] if tenant else [])], {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    def test_set_in_each_layer_writes_private_files_in_private_directories(self) -> None:
        for flags, path in (
            (["--for", "remote-control"], self.root / "remote-control" / "secrets.env"),
            (["--agent", "w1"], self.root / "agents" / "w1" / "secrets.env"),
        ):
            code, out, _ = self.cmd("secret", "set", "MY_KEY", *flags, stdin="value-12345\n")

            self.assertEqual(0, code, flags)
            self.assertNotIn("value-12345", out)
            self.assertEqual({"MY_KEY": "value-12345"}, envfile.read(path))
            self.assertEqual(0o600, stat.S_IMODE(path.stat().st_mode))
            self.assertEqual(0o700, stat.S_IMODE(path.parent.stat().st_mode))

    def test_env_commands_edit_run_env_and_leave_secrets_alone(self) -> None:
        self.cmd("env", "set", "TZ", "--agent", "w1", stdin="UTC\n")

        self.assertEqual({"TZ": "UTC"}, envfile.read(self.root / "agents" / "w1" / "run.env"))
        self.assertFalse((self.root / "agents" / "w1" / "secrets.env").exists())
        self.assertFalse((self.root / "run.env").exists())

    def test_no_flag_is_the_base_layer_and_lists_as_before(self) -> None:
        self.cmd("secret", "set", "MY_KEY", stdin="value-12345\n")

        code, out, _ = self.cmd("secret", "list")

        self.assertEqual(0, code)
        self.assertEqual(["CLAUDE_CODE_OAUTH_TOKEN (empty)", "MY_KEY"], out.split("\n")[:-1])
        self.assertEqual({"CLAUDE_CODE_OAUTH_TOKEN": "", "MY_KEY": "value-12345"}, envfile.read(self.root / "secrets.env"))

    def test_for_and_agent_together_are_refused(self) -> None:
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            self.cmd("secret", "list", "--for", "remote-control", "--agent", "x")

    def test_an_agent_name_that_is_not_a_name_is_refused_and_nothing_is_created(self) -> None:
        code, _, err = self.cmd("secret", "set", "K", "--agent", "../evil", stdin="value-12345\n")

        self.assertEqual(1, code)
        self.assertIn("agent name", err)
        self.assertFalse((self.root / "agents").exists())
        self.assertFalse((self.tmp / "evil").exists())

    def test_an_agent_that_is_not_managed_can_be_prepared(self) -> None:
        code, _, _ = self.cmd("secret", "set", "K", "--agent", "not-added-yet", stdin="value-12345\n")

        self.assertEqual(0, code)

    def test_a_forbidden_name_is_refused_with_nothing_written_at_every_layer(self) -> None:
        for flags in ([], ["--for", "remote-control"], ["--agent", "w1"]):
            for command in ("secret", "env"):
                code, _, err = self.cmd(command, "set", "LD_PRELOAD", *flags, stdin="value-12345\n")
                self.assertEqual(1, code, (command, flags))
                self.assertIn("refusing LD_PRELOAD", err)
        self.assertFalse((self.root / "remote-control").exists())
        self.assertFalse((self.root / "agents").exists())
        self.assertEqual({"CLAUDE_CODE_OAUTH_TOKEN": ""}, envfile.read(self.root / "secrets.env"))

    def test_the_effective_agent_list_names_the_layer_of_each_key_and_never_a_value(self) -> None:
        self.cmd("secret", "set", "BASE_ONLY", stdin=f"{SENTINELS[0]}\n")
        self.cmd("secret", "set", "BOTH", stdin=f"{SENTINELS[0]}\n")
        self.cmd("secret", "set", "BOTH", "--agent", "w1", stdin=f"{SENTINELS[2]}\n")

        code, out, err = self.cmd("secret", "list", "--agent", "w1")

        self.assertEqual(0, code)
        self.assertEqual(
            ["BASE_ONLY [base]", "BOTH [agent]"], sorted(line for line in out.splitlines() if not line.startswith("CLAUDE"))
        )
        for sentinel in SENTINELS:
            self.assertNotIn(sentinel, out + err)

    def test_the_remote_control_list_shows_overrides_and_masks(self) -> None:
        self.cmd("secret", "set", "SHARED", stdin=f"{SENTINELS[0]}\n")
        self.cmd("secret", "set", "SHARED", "--for", "remote-control", stdin=f"{SENTINELS[1]}\n")
        self.cmd("secret", "set", "WORKER_ONLY", stdin=f"{SENTINELS[0]}\n")

        self.assertEqual(0, self.cmd("secret", "mask", "WORKER_ONLY", "--for", "remote-control")[0])
        code, out, err = self.cmd("secret", "list", "--for", "remote-control")

        self.assertEqual(0, code)
        self.assertEqual(["SHARED [remote-control] (overrides base)", "WORKER_ONLY [remote-control] (masks base)"], out.splitlines())
        for sentinel in SENTINELS:
            self.assertNotIn(sentinel, out + err)

    def test_mask_needs_the_remote_control_layer(self) -> None:
        for flags in ([], ["--agent", "w1"]):
            code, _, err = self.cmd("secret", "mask", "KEY", *flags)
            self.assertEqual(1, code)
            self.assertIn("remote-control", err)

    def test_unset_removes_from_that_layer_only(self) -> None:
        self.cmd("secret", "set", "K", stdin="value-base\n")
        self.cmd("secret", "set", "K", "--agent", "w1", stdin="value-agent\n")

        self.assertEqual("removed K", self.cmd("secret", "unset", "K", "--agent", "w1")[1].strip())
        self.assertEqual("K was not set", self.cmd("secret", "unset", "K", "--agent", "w1")[1].strip())
        self.assertEqual("value-base", envfile.read(self.root / "secrets.env")["K"])

    def test_unset_and_list_in_a_layer_that_does_not_exist_create_nothing(self) -> None:
        self.cmd("secret", "unset", "K", "--agent", "w1")
        self.cmd("secret", "list", "--for", "remote-control")

        self.assertFalse((self.root / "agents").exists())
        self.assertFalse((self.root / "remote-control").exists())

    def test_values_stay_out_of_every_output_including_error_paths(self) -> None:
        self.cmd("secret", "set", "K", "--agent", "w1", stdin=f"{SENTINELS[2]}\n")
        (self.root / "agents" / "w1" / "secrets.env").write_text(f"LD_PRELOAD={SENTINELS[2]}\nK={SENTINELS[2]}\n")
        seen = ""
        for argv in (["secret", "list", "--agent", "w1"], ["secret", "set", "LD_PRELOAD", "--agent", "w1"], ["secret", "unset", "LD_PRELOAD", "--agent", "w1"]):
            _, out, err = self.cmd(*argv, stdin=f"{SENTINELS[2]}\n")
            seen += out + err

        self.assertNotIn(SENTINELS[2], seen)

    def test_a_value_too_short_to_mask_is_refused_for_a_secret_but_not_an_env(self) -> None:
        self.assertEqual(1, self.cmd("secret", "set", "K", "--agent", "w1", stdin="abc\n")[0])
        self.assertEqual(0, self.cmd("env", "set", "K", "--agent", "w1", stdin="abc\n")[0])

    def test_env_for_remote_control_edits_the_kind_run_env(self) -> None:
        self.cmd("env", "set", "TZ", "--for", "remote-control", stdin="UTC\n")

        self.assertEqual({"TZ": "UTC"}, envfile.read(self.root / "remote-control" / "run.env"))
        code, out, _ = self.cmd("env", "list", "--for", "remote-control")
        self.assertEqual((0, ["TZ [remote-control]"]), (code, out.splitlines()))
        self.assertFalse((self.root / "remote-control" / "secrets.env").exists())

    def test_unmask_restores_the_base_key_for_remote_control(self) -> None:
        self.cmd("secret", "set", "K", stdin="value-base\n")
        self.cmd("secret", "mask", "K", "--for", "remote-control")
        self.assertIn("K", (self.root / "remote-control" / "secrets.env").read_text())

        code, out, _ = self.cmd("secret", "unmask", "K", "--for", "remote-control")

        self.assertEqual((0, "K is no longer withheld"), (code, out.strip()))
        self.assertNotIn("!K", (self.root / "remote-control" / "secrets.env").read_text())
        self.assertEqual("K was not masked", self.cmd("secret", "unmask", "K", "--for", "remote-control")[1].strip())

    def test_setting_in_an_override_layer_prints_no_caveat(self) -> None:
        _, out, _ = self.cmd("secret", "set", "K", "--agent", "w1", stdin="value-12345\n")
        _, base_out, _ = self.cmd("secret", "set", "K", stdin="value-12345\n")

        self.assertNotIn("note:", out)
        self.assertNotIn("note:", base_out)
