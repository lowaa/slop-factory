from __future__ import annotations

import argparse
import contextlib
import io
import tarfile
import tempfile
import unittest
import unittest.mock
import sys
from pathlib import Path

from slop_haus_supervisor import cli
from slop_haus_supervisor.setup.prerequisites import Finding


def make_archive(path: Path) -> Path:
    body = b'[project]\nname = "slop-haus-supervisor"\nversion = "0.1.0"\n'
    with tarfile.open(path, "w:gz") as tar:
        for name, data in (("pyproject.toml", body), ("uv.lock", b""), ("src/pkg/__init__.py", b"V = 1\n")):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))
    return path


class LauncherCliTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.run_cli(
            "init", "--server", "https://s.example/", "--tenant-id", "tid", "--tenant", "acme",
            "--package", str(make_archive(self.tmp / "package.tar.gz")),
        )  # fmt: skip

    def run_cli(self, *argv: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cli.main([*argv, *([] if argv[0] == "init" else ["-t", "acme"])], {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    def test_stop_of_a_supervisor_that_is_not_running_says_so(self) -> None:
        self.assertEqual((0, "not running\n"), self.run_cli("stop")[:2])

    def test_stop_needs_no_multiplexer(self) -> None:
        with unittest.mock.patch("slop_haus_supervisor.cli.multiplexer.choose", side_effect=cli.LauncherError("neither")):
            self.assertEqual((0, "not running\n"), self.run_cli("stop")[:2])

    def start_in_host_session(self, *flags: str) -> tuple[int, str, unittest.mock.MagicMock]:
        out = io.StringIO()
        with contextlib.redirect_stdout(out), unittest.mock.patch("slop_haus_supervisor.cli.multiplexer.choose") as choose:
            choose.return_value.exists.return_value = False
            choose.return_value.binary = "screen"
            with unittest.mock.patch("slop_haus_supervisor.cli.SupervisorSession.start", return_value=0) as start:
                code = cli.main(["start", "-t", "acme", *flags], {"SLOP_HAUS_HOST_SESSION": "1"}, self.home, self.cwd)
        return code, out.getvalue(), unittest.mock.MagicMock(choose=choose, start=start)

    def test_start_from_an_agent_session_refuses_without_choosing_a_multiplexer(self) -> None:
        code, out, mocks = self.start_in_host_session()

        self.assertEqual(1, code)
        self.assertIn("refusing to start", out)
        mocks.choose.assert_not_called()
        mocks.start.assert_not_called()

    def test_start_from_an_agent_session_with_the_flag_warns_and_starts(self) -> None:
        code, out, mocks = self.start_in_host_session("--allow-host-session")

        self.assertEqual(0, code)
        self.assertIn("warning: this is an agent session", out)
        mocks.start.assert_called_once_with()

    def launch_session(self, unit_installed: bool) -> unittest.mock.MagicMock:
        unit = self.tmp / "unit.service"
        if unit_installed:
            unit.write_text("")
        manager = unittest.mock.MagicMock(unit_path=unit)
        with (
            unittest.mock.patch("slop_haus_supervisor.cli._service_manager", return_value=manager),
            unittest.mock.patch("slop_haus_supervisor.cli.multiplexer.choose_or_none"),
            unittest.mock.patch("slop_haus_supervisor.cli.multiplexer.choose"),
            unittest.mock.patch("slop_haus_supervisor.cli.SupervisorSession.start", autospec=True, return_value=0) as start,
        ):
            self.assertEqual(0, cli.main(["start", "-t", "acme"], {}, self.home, self.cwd))
        return start.call_args.args[0]

    def test_start_acts_on_the_unit_once_one_is_installed(self) -> None:
        session = self.launch_session(unit_installed=True)

        self.assertIsNotNone(session._unit)

    def test_start_uses_the_multiplexer_session_without_an_installed_unit(self) -> None:
        session = self.launch_session(unit_installed=False)

        self.assertIsNone(session._unit)

    def test_logs_before_the_first_start_names_the_fix(self) -> None:
        code, _, err = self.run_cli("logs")

        self.assertEqual(1, code)
        self.assertIn("slop-haus start", err)

    def test_status_says_how_to_start_a_stopped_supervisor(self) -> None:
        code, out, _ = self.run_cli("status")

        self.assertEqual(0, code)
        self.assertIn("slop-haus start", out)

    def test_setup_refuses_a_nonsense_slot_count_before_doing_anything(self) -> None:
        code, _, err = self.run_cli("setup", "--slots", "0")

        self.assertEqual(1, code)
        self.assertIn("--slots", err)

    def sign_supervisor_in(self) -> None:
        credentials = self.home / "slop-haus" / "acme" / "supervisor" / ".slop-haus"
        credentials.mkdir(parents=True)
        (credentials / "credentials.json").write_text("{}")

    def test_slot_reissue_needs_a_slot_or_all(self) -> None:
        self.sign_supervisor_in()
        with unittest.mock.patch.object(cli, "reissue_slot_credentials") as reissue:
            neither = self.run_cli("slot", "reissue")
            both = self.run_cli("slot", "reissue", "a", "--all")

        self.assertEqual((1, 1), (neither[0], both[0]))
        reissue.assert_not_called()

    def test_slot_reissue_needs_the_supervisor_signed_in(self) -> None:
        with unittest.mock.patch.object(cli, "reissue_slot_credentials") as reissue:
            code, _, err = self.run_cli("slot", "reissue", "--all")

        self.assertEqual(1, code)
        self.assertIn("login", err)
        reissue.assert_not_called()

    def test_slot_reissue_passes_the_named_slot_or_none_for_all_and_fails_on_a_refusal(self) -> None:
        self.sign_supervisor_in()
        with unittest.mock.patch.object(cli, "reissue_slot_credentials", return_value=0) as reissue:
            self.assertEqual(0, self.run_cli("slot", "reissue", "a")[0])
            self.assertEqual(0, self.run_cli("slot", "reissue", "--all")[0])
        self.assertEqual([["a"], None], [call.args[2] for call in reissue.call_args_list])

        with unittest.mock.patch.object(cli, "reissue_slot_credentials", return_value=1):
            self.assertEqual(1, self.run_cli("slot", "reissue", "--all")[0])

    def test_setup_adds_no_slots_while_the_board_provisions(self) -> None:
        credentials = self.home / "slop-haus" / "acme" / "supervisor" / ".slop-haus"
        credentials.mkdir(parents=True)
        (credentials / "credentials.json").write_text("{}")
        board = "https://slop.example/workers"

        with (
            unittest.mock.patch.object(sys, "stdin", io.StringIO("tok-value-1234\n\n")),
            unittest.mock.patch.object(cli.Prerequisites, "check", return_value=[Finding("uv", True, "found")]),
            unittest.mock.patch.object(cli.SupervisorGrant, "state", return_value=cli.GrantState.ACTIVE),
            unittest.mock.patch.object(cli, "_slot_add") as slot_add,
            unittest.mock.patch.object(cli, "_board_workers_url", return_value=board) as ask,
        ):
            code, out, _ = self.run_cli("setup", "--slots", "2")

        self.assertEqual(0, code)
        self.assertIn(board, out)
        slot_add.assert_not_called()
        self.assertIsInstance(ask.call_args.args[1], cli.SupervisorApi)

    def test_a_valid_setup_adds_the_missing_slots_and_stores_the_token_through_the_real_wiring(self) -> None:
        root = self.home / "slop-haus" / "acme"
        credentials = root / "supervisor" / ".slop-haus"
        credentials.mkdir(parents=True)
        (credentials / "credentials.json").write_text("{}")
        added: list[int] = []

        def record(args: argparse.Namespace, *_rest: object) -> None:
            added.append(int(args.count))

        ready = [Finding("uv", True, "found")]

        with (
            unittest.mock.patch.object(sys, "stdin", io.StringIO("tok-value-1234\n\n")),
            unittest.mock.patch.object(cli.Prerequisites, "check", return_value=ready),
            unittest.mock.patch.object(cli.SupervisorGrant, "state", return_value=cli.GrantState.ACTIVE),
            unittest.mock.patch.object(cli, "_slot_add", side_effect=record),
            unittest.mock.patch.object(cli, "_board_workers_url", return_value=None),
            unittest.mock.patch.object(cli, "_refuse_while_board_provisions"),
        ):
            code, out, _ = self.run_cli("setup", "--slots", "2")

        self.assertEqual(0, code)
        self.assertEqual([2], added)
        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN=tok-value-1234", (root / "secrets.env").read_text())
        self.assertNotIn("tok-value-1234", out)

    def test_start_and_attach_reach_the_session(self) -> None:
        session = unittest.mock.Mock()
        session.start.return_value = 7
        session.attach_argv.return_value = ["screen", "-r", "x"]
        with (
            unittest.mock.patch.object(cli, "_session", return_value=session),
            unittest.mock.patch.object(cli.subprocess, "call", return_value=5) as call,
        ):
            self.assertEqual(7, self.run_cli("start")[0])
            self.assertEqual(5, self.run_cli("attach")[0])

        call.assert_called_once_with(["screen", "-r", "x"])
