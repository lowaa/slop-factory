from __future__ import annotations

import logging
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex
from slop_haus_supervisor.remote_control.host.launch import Applied, ConfigWatch, LaunchRefused, PythonLaunch
from slop_haus_supervisor.remote_control.host.main import main, parse_args, retune
from slop_haus_supervisor.remote_control.pi.harness import RemoteControlPi
from slop_haus_supervisor.remote_control.wire.errors import Refused
from slop_haus_supervisor.service.run_env import LauncherEnvError
from slop_haus_supervisor.workspace.harness_config import HarnessConfigError


class PythonLaunchTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.dir = self.root / ".slop-haus"
        self.dir.mkdir()
        # No server address: every fetch warns, and the saved copy, if any, stands.
        self.environ: dict[str, str] = {}
        self.logger = logging.getLogger("test-launch")

    def launch(self, harness: str = "claude") -> Applied:
        with self.assertLogs(self.logger, "WARNING"):
            return PythonLaunch(self.root, self.environ, SubprocessRunner(), self.logger).run(harness)

    def save(self, text: str) -> None:
        (self.dir / "harness-config.json").write_text(text)

    def test_stray_slop_haus_variables_go_before_anything_is_read(self) -> None:
        self.environ.update({"SLOP_HAUS_TOKEN": "dead", "SLOP_HAUS_URL": "http://another", "KEEP_ME": "yes"})
        (self.dir / "run.env").write_text("FROM_RUN_ENV=1\n")
        self.launch()
        self.assertEqual({"KEEP_ME": "yes", "FROM_RUN_ENV": "1", "SLOP_HAUS_HARNESS": "claude"}, self.environ)

    def test_token_sh_runs_without_stray_variables_or_run_env(self) -> None:
        seen = self.root / "token.env"
        token = self.dir / "token.sh"
        token.write_text(f"#!/bin/sh\nenv > {seen}\nprintf tok\n")
        token.chmod(0o755)
        (self.dir / "server-url").write_text("http://127.0.0.1:1\n")
        (self.dir / "run.env").write_text("FROM_RUN_ENV=1\n")
        # The real process environment, which is what token.sh inherits.
        patched = mock.patch.dict(os.environ, {"SLOP_HAUS_TOKEN": "dead"})
        patched.start()
        self.addCleanup(patched.stop)
        self.environ = os.environ  # pyright: ignore[reportAttributeAccessIssue]
        self.launch()
        text = seen.read_text()
        self.assertNotIn("SLOP_HAUS_TOKEN", text)
        self.assertNotIn("FROM_RUN_ENV", text)

    def test_run_env_may_not_claim_another_harness(self) -> None:
        (self.dir / "run.env").write_text("SLOP_HAUS_HARNESS=pi\n")
        self.launch()
        self.assertEqual("claude", self.environ["SLOP_HAUS_HARNESS"])

    def test_what_the_config_sets_beats_run_env_and_the_revision_is_exported(self) -> None:
        (self.dir / "run.env").write_text("ANTHROPIC_MODEL=from-run-env\nCLAUDE_CODE_EFFORT_LEVEL=low\n")
        self.save('{"revision": 7, "settings": {"claude": {"model": "opus"}}}')
        self.launch()
        self.assertEqual("opus", self.environ["ANTHROPIC_MODEL"])
        self.assertEqual("low", self.environ["CLAUDE_CODE_EFFORT_LEVEL"])
        self.assertEqual("7", self.environ["SLOP_HAUS_CONFIG_REVISION"])

    def test_pi_gets_its_config_as_flags_not_variables(self) -> None:
        self.save('{"revision": 1, "settings": {"pi": {"model": "m"}, "claude": {"model": "opus"}}}')
        self.assertEqual(Applied({"model": "m"}), self.launch("pi"))
        self.assertNotIn("ANTHROPIC_MODEL", self.environ)
        self.assertEqual("pi", self.environ["SLOP_HAUS_HARNESS"])

    def test_no_saved_config_leaves_run_env_alone_to_apply(self) -> None:
        (self.dir / "run.env").write_text("ANTHROPIC_MODEL=from-run-env\n")
        self.assertEqual(Applied(), self.launch())
        self.assertEqual("from-run-env", self.environ["ANTHROPIC_MODEL"])
        self.assertNotIn("SLOP_HAUS_CONFIG_REVISION", self.environ)

    def test_an_agent_in_a_variant_is_not_started_on_another_harness(self) -> None:
        self.save('{"revision": 1, "settings": {}, "harness": "pi"}')
        with self.assertRaisesRegex(LaunchRefused, "runs pi, not claude.*--harness pi"):
            self.launch("claude")
        self.launch("pi")

    def test_a_config_naming_no_harness_or_an_unknown_one_holds_the_agent_to_none(self) -> None:
        for extra in ("", ', "harness": "other"'):
            self.save('{"revision": 1, "settings": {}' + extra + "}")
            self.launch("pi")
            self.launch("claude")

    # -- a change on the board, taken up between turns --

    def started(self, config: str, harness: str = "claude") -> PythonLaunch:
        self.save(config)
        launch = PythonLaunch(self.root, self.environ, SubprocessRunner(), self.logger)
        with self.assertLogs(self.logger, "WARNING"):
            launch.run(harness)
        return launch

    def reload(self, launch: PythonLaunch, harness: str = "claude") -> Applied | None:
        with self.assertLogs(self.logger, "WARNING"):
            return launch.reload(harness)

    def test_the_same_revision_again_changes_nothing(self) -> None:
        launch = self.started('{"revision": 7, "settings": {"claude": {"model": "sonnet"}}}')

        self.assertIsNone(self.reload(launch))
        self.assertEqual("sonnet", self.environ["ANTHROPIC_MODEL"])

    def test_a_new_revision_is_put_in_place_for_the_next_harness(self) -> None:
        launch = self.started('{"revision": 7, "settings": {"claude": {"model": "sonnet"}}}')
        self.save('{"revision": 8, "settings": {"claude": {"model": "opus", "effort": "high"}}}')

        self.assertEqual(Applied(), self.reload(launch))
        self.assertEqual("opus", self.environ["ANTHROPIC_MODEL"])
        self.assertEqual("high", self.environ["CLAUDE_CODE_EFFORT_LEVEL"])
        self.assertEqual("8", self.environ["SLOP_HAUS_CONFIG_REVISION"])

    def test_a_setting_the_board_drops_gives_back_what_was_underneath(self) -> None:
        (self.dir / "run.env").write_text("ANTHROPIC_MODEL=from-run-env\n")
        launch = self.started('{"revision": 7, "settings": {"claude": {"model": "opus", "effort": "high"}}}')
        # A change in between: what is given back is what was there before the config, not its last value.
        self.save('{"revision": 8, "settings": {"claude": {"model": "sonnet", "effort": "low"}}}')
        self.reload(launch)
        self.save('{"revision": 9, "settings": {}}')

        self.reload(launch)

        self.assertEqual("from-run-env", self.environ["ANTHROPIC_MODEL"], "run.env decides again")
        self.assertNotIn("CLAUDE_CODE_EFFORT_LEVEL", self.environ, "nothing was underneath")

    def test_a_config_that_is_gone_gives_back_everything_and_the_revision(self) -> None:
        launch = self.started('{"revision": 7, "settings": {"claude": {"model": "opus"}}}')
        (self.dir / "harness-config.json").unlink()

        self.assertEqual(Applied(), self.reload(launch))
        self.assertNotIn("ANTHROPIC_MODEL", self.environ)
        self.assertNotIn("SLOP_HAUS_CONFIG_REVISION", self.environ)

    def test_pis_new_flags_come_back_to_be_given_to_the_harness(self) -> None:
        launch = self.started('{"revision": 1, "settings": {"pi": {"model": "m1"}}}', "pi")
        self.save('{"revision": 2, "settings": {"pi": {"model": "m2"}}}')

        self.assertEqual(Applied({"model": "m2"}), self.reload(launch, "pi"))

    def test_codexs_variant_comes_back_at_start_and_after_a_reload(self) -> None:
        launch = PythonLaunch(self.root, self.environ, SubprocessRunner(), self.logger)
        self.save('{"revision": 1, "settings": {"codex": {"model": "m1", "effort": "low"}}}')
        with self.assertLogs(self.logger, "WARNING"):
            self.assertEqual("model=m1,effort=low", launch.run("codex").variant)
        self.save('{"revision": 2, "settings": {"codex": {"model": "m2"}}}')
        self.assertEqual("model=m2", (self.reload(launch, "codex") or Applied()).variant)

    def test_a_codex_effort_codex_would_refuse_stops_the_launch(self) -> None:
        self.save('{"revision": 1, "settings": {"codex": {"effort": "extreme"}}}')
        with self.assertRaises(HarnessConfigError):
            self.launch("codex")

    def test_a_refused_codex_value_on_reload_leaves_the_applied_revision_alone(self) -> None:
        launch = PythonLaunch(self.root, self.environ, SubprocessRunner(), self.logger)
        self.save('{"revision": 1, "settings": {"codex": {"effort": "low"}}}')
        with self.assertLogs(self.logger, "WARNING"):
            launch.run("codex")
        self.save('{"revision": 2, "settings": {"codex": {"effort": "extreme"}}}')
        with self.assertLogs(self.logger, "WARNING"), self.assertRaises(HarnessConfigError):
            launch.reload("codex")
        self.assertEqual("1", self.environ["SLOP_HAUS_CONFIG_REVISION"])

    def test_a_move_to_another_harness_is_not_taken_up_in_this_process(self) -> None:
        launch = self.started('{"revision": 7, "settings": {"claude": {"model": "sonnet"}}}')
        self.save('{"revision": 8, "settings": {"claude": {"model": "opus"}}, "harness": "pi"}')

        with self.assertLogs(self.logger, "WARNING") as said:
            self.assertIsNone(launch.reload("claude"))
        self.assertIn("now runs pi", "\n".join(said.output))
        self.assertEqual("sonnet", self.environ["ANTHROPIC_MODEL"])

    def test_a_refused_run_env_stops_the_launch_and_sets_nothing_from_it(self) -> None:
        (self.dir / "run.env").write_text("GOOD=1\nBASH_ENV=/x\n")
        with self.assertRaisesRegex(LauncherEnvError, "refusing BASH_ENV"):
            self.launch()
        self.assertNotIn("GOOD", self.environ)


class MainTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = tmp.name

    def run_main(self, *argv: str, launch: mock.Mock) -> tuple[int, mock.Mock]:
        (Path(self.root) / ".slop-haus").mkdir()
        (Path(self.root) / ".slop-haus" / "server-url").write_text("http://x\n")
        stop = mock.Mock(side_effect=Refused("stop here"))
        with (
            mock.patch("slop_haus_supervisor.remote_control.host.main.PythonLaunch", return_value=launch),
            mock.patch("slop_haus_supervisor.remote_control.host.main.choose_harness", stop),
        ):
            return main(["--workspace", self.root, *argv]), stop

    def test_the_launcher_steps_are_not_run_without_the_flag(self) -> None:
        launch = mock.Mock()
        status, _ = self.run_main(launch=launch)
        self.assertEqual(1, status)
        launch.run.assert_not_called()

    def test_the_configs_pi_flags_reach_the_harness_unless_given_on_the_command_line(self) -> None:
        launch = mock.Mock()
        launch.run.return_value = Applied({"provider": "openrouter", "model": "from-config"})
        _, chosen = self.run_main("--python-launch", "--harness", "pi", "--model", "given", launch=launch)
        launch.run.assert_called_once_with("pi")
        args = chosen.call_args.args[0]
        self.assertEqual(("openrouter", "given", ""), (args.provider, args.model, args.thinking))

    def test_the_configs_codex_variant_reaches_the_harness(self) -> None:
        launch = mock.Mock()
        launch.run.return_value = Applied(variant="model=m,effort=low")
        _, chosen = self.run_main("--python-launch", "--harness", "codex", launch=launch)
        self.assertEqual("model=m,effort=low", chosen.call_args.kwargs["codex_variant"])

    def test_a_refused_launch_stops_before_a_harness_is_chosen(self) -> None:
        launch = mock.Mock()
        launch.run.side_effect = LaunchRefused("not this harness")
        status, chosen = self.run_main("--python-launch", launch=launch)
        self.assertEqual(1, status)
        chosen.assert_not_called()


class FlagTest(unittest.TestCase):
    def test_python_launch_is_off_by_default(self) -> None:
        self.assertFalse(parse_args([]).python_launch)
        self.assertTrue(parse_args(["--python-launch"]).python_launch)


if __name__ == "__main__":
    unittest.main()


class ConfigWatchTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / ".slop-haus").mkdir()
        self.logger = logging.getLogger("test-watch")
        self.launch = PythonLaunch(self.root, {}, SubprocessRunner(), self.logger)
        self.flags: list[Applied] = []
        self.watch = ConfigWatch(self.launch, "pi", self.flags.append)

    def save(self, text: str) -> None:
        (self.root / ".slop-haus" / "harness-config.json").write_text(text)

    def test_a_change_hands_the_harness_its_flags_once(self) -> None:
        self.save('{"revision": 1, "settings": {"pi": {"model": "m1"}}}')
        with self.assertLogs(self.logger, "WARNING"):
            self.assertTrue(self.watch.changed())
            self.assertFalse(self.watch.changed())
        self.assertEqual([Applied({"model": "m1"})], self.flags)

    def test_a_config_that_cannot_be_read_keeps_the_one_the_session_has(self) -> None:
        with mock.patch.object(self.launch, "reload", side_effect=OSError("disk gone")):
            with self.assertLogs(self.logger, "WARNING") as said:
                self.assertFalse(self.watch.changed())
        self.assertIn("keeps the one it has", "\n".join(said.output))
        self.assertEqual([], self.flags)


class RetuneTest(unittest.TestCase):
    def test_codexs_next_sessions_take_the_new_variant(self) -> None:
        codex = RemoteControlCodex(Path("/state"), "http://x", "prompt", "model=old")
        retune(codex, {"provider": "", "model": "", "thinking": ""}, Applied(variant="model=new,effort=high"))
        self.assertEqual("model=new,effort=high", codex._variant)  # pyright: ignore[reportPrivateUsage]

    def test_a_config_without_codex_settings_goes_back_to_codexs_defaults(self) -> None:
        codex = RemoteControlCodex(Path("/state"), "http://x", "prompt", "model=old")
        retune(codex, {"provider": "", "model": "", "thinking": ""}, Applied())
        self.assertEqual("", codex._variant)  # pyright: ignore[reportPrivateUsage]

    def pi(self) -> RemoteControlPi:
        return RemoteControlPi(Path("/state"), Path("/ext.ts"), "prompt", "model=old")

    def test_pis_next_sessions_take_the_new_flags_but_a_command_line_flag_still_wins(self) -> None:
        pi = self.pi()
        retune(pi, {"provider": "", "model": "", "thinking": "high"}, Applied({"model": "new", "thinking": "low"}))
        self.assertEqual("model=new,thinking=high", pi._variant)  # pyright: ignore[reportPrivateUsage]

    def test_a_flag_pi_would_refuse_is_not_taken_up(self) -> None:
        pi = self.pi()
        with self.assertLogs("slop_haus_supervisor.remote_control.host.main", "WARNING"):
            retune(pi, {"provider": "", "model": "", "thinking": ""}, Applied({"thinking": "nonsense"}))
        self.assertEqual("model=old", pi._variant)  # pyright: ignore[reportPrivateUsage]
