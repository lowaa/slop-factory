from __future__ import annotations

import os
import shutil
import subprocess
import unittest.mock
from pathlib import Path
from typing import cast

from slop_haus_supervisor import cli
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace.clone_probe import ProbeResult
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace import rebuild
from slop_haus_supervisor.workspace.handover import GitSlotRepo
from slop_haus_supervisor.workspace.update_script import UpdateScript
from slop_haus_supervisor.workspace.layout import Layout, SlotService
from support import TempDirTestCase, make_archive

from test_layout import CONFIG, git


def spawn(argv: list[str], cwd: Path) -> int:
    return subprocess.run(argv, cwd=cwd, check=False).returncode


class FakeService:
    def __init__(self, script: str, live: bool = True, adoptable: bool = False) -> None:
        self.script, self.live, self.adoptable = script, live, adoptable
        self.adopted: list[str] = []

    def find_slot(self, name: str) -> SlotRef | None:
        return SlotRef("id", name) if self.live else None

    def adopt_slot(self, name: str) -> SlotRef:
        if not self.adoptable:
            raise ApiError("not found", 404)
        self.adopted.append(name)
        self.live = True
        return SlotRef("id", name)

    def update_script(self, tokens: TokenPair) -> str:
        return self.script


class FakeProbe:
    def __init__(self, lease: bool = False) -> None:
        self.lease = lease

    def access_token(self, directory: Path) -> str:
        return "access"

    def lease_held(self, access_token: str) -> bool:
        return self.lease


class RebuildCliTest(unittest.TestCase):
    def test_all_and_a_name_are_exclusive(self) -> None:
        for argv in (["slot", "rebuild"], ["slot", "rebuild", "w1", "--all"]):
            with self.subTest(argv=argv):
                args = cli._parser().parse_args(argv)  # pyright: ignore[reportPrivateUsage]
                with self.assertRaisesRegex(cfg.ConfigError, "name, or --all"):
                    cli._slot_rebuild(args, Layout(Path("/nonexistent")), None, {})  # pyright: ignore[reportPrivateUsage]


class RebuildAllTest(TempDirTestCase):
    def test_all_tries_every_slot_prints_each_outcome_and_fails_if_any_refused(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        layout.config_file.write_text(cfg.load(layout.config_file).with_slot("w1").with_slot("w2").dumps())
        args = cli._parser().parse_args(["slot", "rebuild", "--all"])  # pyright: ignore[reportPrivateUsage]

        def fake(layout: Layout, name: str, *rest: object, **kw: object) -> rebuild.Rebuilt:
            if name == "w1":
                raise cfg.ConfigError("no live slot")
            return rebuild.Rebuilt(name, True)

        with (
            unittest.mock.patch.object(rebuild, "rebuild_slot", side_effect=fake) as mocked,
            unittest.mock.patch("sys.stdout") as out,
            unittest.mock.patch("sys.stderr") as err,
        ):
            code = cli._slot_rebuild(args, layout, None, {})  # pyright: ignore[reportPrivateUsage]

        self.assertEqual(1, code)
        self.assertEqual(2, mocked.call_count)
        self.assertIn("slot w2 rebuilt", "".join(c.args[0] for c in out.write.call_args_list))
        self.assertIn("slot w1 NOT rebuilt: no live slot", "".join(c.args[0] for c in err.write.call_args_list))

    def test_an_api_error_on_one_slot_does_not_stop_the_rest(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        layout.config_file.write_text(cfg.load(layout.config_file).with_slot("w1").with_slot("w2").dumps())
        args = cli._parser().parse_args(["slot", "rebuild", "--all"])  # pyright: ignore[reportPrivateUsage]
        answers: list[object] = [ApiError("down", 503), rebuild.Rebuilt("w2", True)]

        with (
            unittest.mock.patch.object(rebuild, "rebuild_slot", side_effect=answers) as mocked,
            unittest.mock.patch("sys.stderr"),
        ):
            code = cli._slot_rebuild(args, layout, None, {})  # pyright: ignore[reportPrivateUsage]

        self.assertEqual((1, 2), (code, mocked.call_count))

    def test_a_named_slot_still_raises(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        layout.config_file.write_text(cfg.load(layout.config_file).with_slot("w1").dumps())
        args = cli._parser().parse_args(["slot", "rebuild", "w1"])  # pyright: ignore[reportPrivateUsage]

        with (
            unittest.mock.patch.object(rebuild, "rebuild_slot", side_effect=cfg.ConfigError("no")),
            self.assertRaises(cfg.ConfigError),
        ):
            cli._slot_rebuild(args, layout, None, {})  # pyright: ignore[reportPrivateUsage]


class RebuildCliDispatchTest(TempDirTestCase):
    def test_all_rebuilds_every_slot_and_passes_keep_old(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        layout.config_file.write_text(cfg.load(layout.config_file).with_slot("w1").with_slot("w2").dumps())
        args = cli._parser().parse_args(["slot", "rebuild", "--all", "--keep-old"])  # pyright: ignore[reportPrivateUsage]

        with unittest.mock.patch.object(rebuild, "rebuild_slot", return_value=rebuild.Rebuilt("x", True)) as mocked:
            cli._slot_rebuild(args, layout, None, {})  # pyright: ignore[reportPrivateUsage]

        self.assertEqual(["w1", "w2"], [call.args[1] for call in mocked.call_args_list])
        self.assertTrue(all(call.kwargs["keep_old"] for call in mocked.call_args_list))


class RebuildSlotTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "acme")
        lay.set_up(self.layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        self.origin = self.tmp / "origin.git"
        git(self.tmp, "init", "-q", "--bare", "-b", "main", str(self.origin))
        seed = self.tmp / "seed"
        git(self.tmp, "init", "-q", "-b", "main", str(seed))
        (seed / "CLAUDE.md").write_text("hi\n")
        git(seed, "add", ".")
        git(seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
        git(seed, "push", "-q", str(self.origin), "main")
        env = {
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": f"url.{self.origin}.insteadOf",
            "GIT_CONFIG_VALUE_0": "https://git.example/acme/app.git",
            "SLOP_HAUS_CLONE_PROTOCOL": "https",
            "ORIGIN": str(self.origin),
        }
        patcher = unittest.mock.patch.dict(os.environ, env)
        patcher.start()
        self.addCleanup(patcher.stop)

        # A `.`-layout slot: the root is the clone, with the agent's files beside the repo's.
        self.slot = self.layout.slot("w1")
        git(self.tmp, "clone", "-q", str(self.origin), str(self.slot))
        (self.slot / ".slop-haus.env").write_text("SLOP_HAUS_AGENT=w1\n")
        (self.slot / ".slop-haus").mkdir()
        (self.slot / ".slop-haus" / "credentials.json").write_text('{"refresh_token": "r"}')
        (self.slot / ".slop-haus" / "server-url").write_text("https://slop-haus.example\n")
        (self.slot / ".slop-haus" / "stale.sh").write_text("old\n")
        (self.slot / ".env").write_text("K=old\n")
        self.layout.config_file.write_text(cfg.load(self.layout.config_file).with_slot("w1").dumps())
        self.layout.repo_env_file.write_text("WOODPECKER_TOKEN=abc\n")
        self.nested = UpdateScript(
            "touch update-ran\n"
            '"$workspace"/.slop-haus/sync-repos.sh -- \'app\' \'git.example/acme/app\' || echo "not every repo" >&2\n'
            'git clone -q https://git.example/acme/app.git app\n',
            (("app", "git.example/acme/app"),),
        )

    def rebuild(
        self, script: str | None = None, lease: bool = False, keep_old: bool = False, ssh: bool = False
    ) -> rebuild.Rebuilt:
        service = cast(SlotService, FakeService(self.nested if script is None else script))
        return rebuild.rebuild_slot(
            self.layout, "w1", service, GitSlotRepo(SubprocessRunner()), FakeProbe(lease), spawn,
            keep_old=keep_old, ssh_works=lambda url: ProbeResult(ssh, "test"),
        )  # fmt: skip

    def old_dirs(self) -> list[Path]:
        return [p for p in self.layout.slots.iterdir() if ".pre-rebuild-" in p.name]

    def test_a_root_clone_becomes_a_nested_layout_keeping_the_agent_and_sign_in(self) -> None:
        done = self.rebuild()

        self.assertTrue(done.changed)
        self.assertFalse((self.slot / ".git").exists())
        self.assertEqual("hi\n", (self.slot / "app" / "CLAUDE.md").read_text())
        self.assertEqual("https://git.example/acme/app.git", git(self.slot / "app", "config", "--get", "remote.origin.url"))
        self.assertEqual("SLOP_HAUS_AGENT=w1\n", (self.slot / ".slop-haus.env").read_text())
        self.assertEqual('{"refresh_token": "r"}', (self.slot / ".slop-haus" / "credentials.json").read_text())
        self.assertTrue((self.slot / "update-ran").is_file())
        self.assertFalse((self.slot / ".slop-haus" / "stale.sh").exists())
        self.assertEqual("WOODPECKER_TOKEN=abc\n", (self.slot / "app" / ".env").read_text())
        self.assertEqual([], self.old_dirs())

    def test_keep_old_leaves_the_previous_directory(self) -> None:
        done = self.rebuild(keep_old=True)

        self.assertEqual(self.old_dirs(), [done.kept])
        self.assertTrue((self.old_dirs()[0] / ".git").exists())

    def test_unpushed_work_refuses_and_changes_nothing(self) -> None:
        (self.slot / "wip.txt").write_text("mine\n")
        git(self.slot, "add", ".")
        git(self.slot, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "wip")

        with self.assertRaisesRegex(cfg.ConfigError, "not pushed"):
            self.rebuild()
        self.assertEqual("wip", git(self.slot, "log", "-1", "--format=%s"))
        self.assertEqual([], self.old_dirs())

    def test_a_held_lease_refuses(self) -> None:
        with self.assertRaisesRegex(cfg.ConfigError, "lease"):
            self.rebuild(lease=True)
        self.assertTrue((self.slot / ".git").exists())

    def hold_old_lock(self) -> subprocess.Popen[str]:
        """What a token.sh refresh in flight does: hold the slot's credentials lock until told to stop."""
        lock = self.slot / ".slop-haus" / "credentials.lock"
        holder = subprocess.Popen(
            ["bash", "-c", f'exec 9> "{lock}"; flock 9; echo held; read _'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
        )  # fmt: skip
        assert holder.stdout is not None
        self.assertEqual("held", holder.stdout.readline().strip())
        # Registered last-in-first-out: let go, reap, then close the pipe -- so a failing test cannot hang here.
        self.addCleanup(holder.stdout.close)
        self.addCleanup(holder.wait)
        self.addCleanup(lambda: holder.stdin and holder.stdin.close())
        return holder

    def test_a_rebuild_waits_for_a_refresh_in_flight_and_keeps_what_it_wrote(self) -> None:
        holder = self.hold_old_lock()
        r2 = '{"refresh_token": "R2"}'
        calls = 0

        def finish_refresh(_seconds: float) -> None:
            # token.sh's write_credentials, then it lets go of the lock.
            nonlocal calls
            calls += 1
            (self.slot / ".slop-haus" / "credentials.json").write_text(r2)
            assert holder.stdin is not None
            holder.stdin.close()

        with unittest.mock.patch("slop_haus_supervisor.slophaus.credentials.time.sleep", finish_refresh):
            self.rebuild()

        self.assertGreaterEqual(calls, 1)
        self.assertEqual(r2, (self.slot / ".slop-haus" / "credentials.json").read_text())

    def test_a_refresh_that_never_lets_go_refuses_the_rebuild_and_changes_nothing(self) -> None:
        self.hold_old_lock()

        with unittest.mock.patch("slop_haus_supervisor.slophaus.credentials.LOCK_TIMEOUT", 0.2):
            with self.assertRaisesRegex(cfg.ConfigError, "timed out"):
                self.rebuild()

        self.assertTrue((self.slot / ".git").exists())
        self.assertEqual([], self.old_dirs())

    def test_the_old_lock_is_held_across_the_rename_copy_and_install(self) -> None:
        seen: list[int] = []

        def probe_lock(argv: list[str], cwd: Path) -> int:
            # A token.sh starting now (in the interval between the rename and the end of install) finds the
            # old directory's lock taken.
            for old in self.old_dirs():
                lock = old / ".slop-haus" / "credentials.lock"
                seen.append(subprocess.run(["flock", "-n", str(lock), "true"], check=False).returncode)
            return spawn(argv, cwd)

        self.rebuild_with(probe_lock)

        self.assertTrue(seen)
        self.assertTrue(all(code != 0 for code in seen), seen)

    def test_the_new_directorys_lock_is_held_while_the_sign_in_is_copied(self) -> None:
        seen: list[int] = []
        real_copy = shutil.copy2

        def probe_then_copy(source: Path, target: Path) -> object:
            # A token.sh starting now resolves the new path.
            lock = self.slot / ".slop-haus" / "credentials.lock"
            seen.append(subprocess.run(["flock", "-n", str(lock), "true"], check=False).returncode)
            return real_copy(source, target)

        with unittest.mock.patch.object(rebuild.shutil, "copy2", probe_then_copy):
            self.rebuild()

        self.assertTrue(seen)
        self.assertTrue(all(code != 0 for code in seen), seen)

    def rebuild_with(self, run: lay.Interactive) -> rebuild.Rebuilt:
        return rebuild.rebuild_slot(
            self.layout, "w1", cast(SlotService, FakeService(self.nested)), GitSlotRepo(SubprocessRunner()), FakeProbe(), run,
            keep_old=True, ssh_works=lambda url: ProbeResult(False, "test"),
        )  # fmt: skip

    def test_a_running_supervisor_refuses(self) -> None:
        import fcntl

        with (self.layout.root / ".run.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            with self.assertRaisesRegex(cfg.ConfigError, "running"):
                self.rebuild()
        self.assertTrue((self.slot / ".git").exists())

    def test_running_it_twice_leaves_the_second_alone(self) -> None:
        self.rebuild()
        (self.slot / "app" / "marker").write_text("x")

        again = self.rebuild()

        self.assertFalse(again.changed)
        self.assertTrue((self.slot / "app" / "marker").exists())

    def test_no_sign_in_or_no_live_slot_refuses_and_changes_nothing(self) -> None:
        for service in (None, FakeService(self.nested, live=False)):
            with self.assertRaises(cfg.ConfigError):
                rebuild.rebuild_slot(
                    self.layout, "w1", cast("SlotService | None", service), GitSlotRepo(SubprocessRunner()), FakeProbe(), spawn
                )
            self.assertTrue((self.slot / ".git").exists())
            self.assertEqual([], self.old_dirs())

    def test_a_reused_agent_without_a_supervisor_is_adopted_then_rebuilt(self) -> None:
        service = FakeService(self.nested, live=False, adoptable=True)

        result = rebuild.rebuild_slot(self.layout, "w1", cast(SlotService, service), GitSlotRepo(SubprocessRunner()), FakeProbe(), spawn)

        self.assertEqual(["w1"], service.adopted)
        self.assertTrue(result.changed)

    def test_a_slot_the_server_already_has_is_not_adopted(self) -> None:
        service = FakeService(self.nested, adoptable=True)

        rebuild.rebuild_slot(self.layout, "w1", cast(SlotService, service), GitSlotRepo(SubprocessRunner()), FakeProbe(), spawn)

        self.assertEqual([], service.adopted)

    def test_a_refused_adoption_says_so_and_changes_nothing(self) -> None:
        service = FakeService(self.nested, live=False)

        with self.assertRaisesRegex(cfg.ConfigError, "could not adopt.*not found"):
            rebuild.rebuild_slot(self.layout, "w1", cast(SlotService, service), GitSlotRepo(SubprocessRunner()), FakeProbe(), spawn)

    def test_the_old_origin_picks_the_folder_in_a_multi_repo_manifest(self) -> None:
        other = self.tmp / "other.git"
        git(self.tmp, "init", "-q", "--bare", str(other))
        script = (
            "\"$workspace\"/.slop-haus/sync-repos.sh -- 'deploy' 'git.example/acme/deploy' 'app' 'git.example/acme/app' || echo x >&2\n"
            'git clone -q https://git.example/acme/app.git app\ngit init -q deploy\n'
            'git -C deploy remote add origin https://git.example/acme/deploy.git\n'
        )
        script = UpdateScript(script, (("deploy", "git.example/acme/deploy"), ("app", "git.example/acme/app")))
        git(self.slot, "remote", "set-url", "origin", "https://git.example/acme/app.git")

        self.rebuild(script)

        self.assertEqual("WOODPECKER_TOKEN=abc\n", (self.slot / "app" / ".env").read_text())
        self.assertFalse((self.slot / "deploy" / ".env").exists())

    def test_a_failed_update_puts_the_old_directory_back(self) -> None:
        with self.assertRaises(cfg.ConfigError):
            self.rebuild(UpdateScript(self.nested + "exit 1\n", self.nested.repos))

        self.assertTrue((self.slot / ".git").exists())
        self.assertEqual("SLOP_HAUS_AGENT=w1\n", (self.slot / ".slop-haus.env").read_text())
        self.assertEqual([], self.old_dirs())
        self.assertFalse((self.slot / "app").exists())

    def ssh_only_script(self) -> UpdateScript:
        # What sync-repos.sh does with the protocol it is handed; the fake remote answers only over ssh.
        return UpdateScript(
            "\"$workspace\"/.slop-haus/sync-repos.sh -- 'app' 'git.example/acme/app' || echo x >&2\n"
            'echo "$SLOP_HAUS_CLONE_PROTOCOL" > protocol\n'
            'if [ "$SLOP_HAUS_CLONE_PROTOCOL" = ssh ]; then git clone -q git@git.example:acme/app.git app; fi\n',
            (("app", "git.example/acme/app"),),
        )

    def use_ssh_only_remote(self) -> None:
        os.environ.pop("SLOP_HAUS_CLONE_PROTOCOL")
        os.environ["GIT_CONFIG_KEY_0"] = f"url.{self.origin}.insteadOf"
        os.environ["GIT_CONFIG_VALUE_0"] = "git@git.example:acme/app.git"

    def test_with_no_sibling_clone_an_ssh_only_remote_is_cloned_over_ssh(self) -> None:
        self.use_ssh_only_remote()

        self.rebuild(self.ssh_only_script(), ssh=True)

        self.assertEqual("ssh\n", (self.slot / "protocol").read_text())
        self.assertEqual("hi\n", (self.slot / "app" / "CLAUDE.md").read_text())

    def test_a_stored_protocol_is_used_without_probing(self) -> None:
        self.use_ssh_only_remote()
        self.layout.config_file.write_text(cfg.load(self.layout.config_file).with_clone_protocol("ssh").dumps())

        self.rebuild(self.ssh_only_script(), ssh=False)

        self.assertEqual("ssh\n", (self.slot / "protocol").read_text())

    def reworded(self, script: str) -> UpdateScript:
        """The script with its sync-repos call in a quoting that is no manifest to parse: only the repos beside it say what to clone."""
        text = script.replace("sync-repos.sh -- 'app' 'git.example/acme/app'", "sync-repos.sh --repo=app:git.example/acme/app")
        self.assertNotEqual(script, text)
        return UpdateScript(text, (("app", "git.example/acme/app"),))

    def test_the_repo_list_beside_the_script_is_what_rebuild_reads(self) -> None:
        done = self.rebuild(self.reworded(self.nested))

        self.assertTrue(done.changed)
        self.assertEqual("hi\n", (self.slot / "app" / "CLAUDE.md").read_text())

    def test_the_repo_list_beside_the_script_is_what_the_protocol_probe_reads(self) -> None:
        self.use_ssh_only_remote()

        self.rebuild(self.reworded(self.ssh_only_script()), ssh=True)

        self.assertEqual("ssh\n", (self.slot / "protocol").read_text())

    def test_a_script_that_syncs_repos_without_a_list_is_refused(self) -> None:
        self.use_ssh_only_remote()

        with self.assertRaisesRegex(cfg.ConfigError, "sent no repo list"):
            self.rebuild(str(self.ssh_only_script()), ssh=True)

    def test_a_failed_clone_fails_the_rebuild_and_restores_the_old_directory(self) -> None:
        self.use_ssh_only_remote()

        with self.assertRaisesRegex(cfg.ConfigError, "not a checkout of git.example/acme/app.*old directory is back"):
            self.rebuild(self.ssh_only_script(), ssh=False)

        self.assertTrue((self.slot / ".git").exists())
        self.assertEqual("SLOP_HAUS_AGENT=w1\n", (self.slot / ".slop-haus.env").read_text())
        self.assertEqual([], self.old_dirs())
        self.assertFalse((self.slot / "protocol").exists())

    def test_a_failed_clone_restores_the_old_directory_with_keep_old_too(self) -> None:
        self.use_ssh_only_remote()

        with self.assertRaises(cfg.ConfigError):
            self.rebuild(self.ssh_only_script(), ssh=False, keep_old=True)

        self.assertTrue((self.slot / ".git").exists())
        self.assertEqual([], self.old_dirs())


if __name__ == "__main__":
    unittest.main()
