from __future__ import annotations

import contextlib
import fcntl
import io
from pathlib import Path

from slop_haus_supervisor import cli
from slop_haus_supervisor.workspace import config as cfg
from support import TempDirTestCase, make_archive


class CliRootsTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.archive = make_archive(self.tmp / "package.tar.gz")

    def run_cli(self, *argv: str, env: dict[str, str] | None = None) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cli.main(list(argv), env or {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    def setup_tenant(self, name: str, tenant_id: str) -> tuple[int, str, str]:
        return self.run_cli(
            "init", "--server", "https://s", "--tenant-id", tenant_id, "--default-tenant", name, "--package", str(self.archive)
        )

    def root(self, name: str):  # noqa: ANN201
        return self.home / "slop-haus" / name

    def test_setup_without_a_tenant_option_uses_the_servers_name(self) -> None:
        code, _, _ = self.setup_tenant("acme", "t1")

        self.assertEqual(0, code)
        self.assertEqual("t1", cfg.load(self.root("acme") / "supervisor.toml").tenant_id)

    def test_setup_again_for_the_same_tenant_refreshes_the_same_root(self) -> None:
        self.setup_tenant("acme", "t1")

        self.setup_tenant("acme", "t1")

        self.assertFalse(self.root("acme-2").exists())

    def test_a_same_named_tenant_with_another_id_gets_its_own_root_and_the_first_is_untouched(self) -> None:
        self.setup_tenant("acme", "t1")
        before = {p: p.read_bytes() for p in self.root("acme").rglob("*") if p.is_file()}

        code, out, _ = self.setup_tenant("acme", "t2")

        self.assertEqual(0, code)
        self.assertIn("acme-2", out)
        self.assertEqual("t2", cfg.load(self.root("acme-2") / "supervisor.toml").tenant_id)
        self.assertEqual("acme-2", cfg.load(self.root("acme-2") / "supervisor.toml").tenant)
        self.assertEqual("t1", cfg.load(self.root("acme") / "supervisor.toml").tenant_id)
        self.assertEqual(before, {p: p.read_bytes() for p in self.root("acme").rglob("*") if p.is_file()})

    def test_a_third_clash_counts_on(self) -> None:
        for number, tenant_id in enumerate(("t1", "t2", "t3")):
            self.setup_tenant("acme", tenant_id)
            self.assertEqual(tenant_id, cfg.load(self.root("acme" if number == 0 else f"acme-{number + 1}") / "supervisor.toml").tenant_id)

    def test_a_fixed_root_in_the_environment_that_holds_another_tenant_is_refused_not_looped_on(self) -> None:
        fixed = {"SLOP_HAUS_HOME": str(self.tmp / "fixed")}
        self.run_cli(
            "init", "--server", "https://s", "--tenant-id", "t1", "--tenant", "acme", "--package", str(self.archive), env=fixed
        )

        code, _, err = self.run_cli(
            "init", "--server", "https://s", "--tenant-id", "t2", "--default-tenant", "acme", "--package", str(self.archive), env=fixed
        )

        self.assertEqual(1, code)
        self.assertIn("belongs to tenant t1", err)

    def test_an_explicit_tenant_is_not_renamed_and_a_clash_is_refused(self) -> None:
        self.setup_tenant("acme", "t1")

        code, _, err = self.run_cli(
            "init", "--server", "https://s", "--tenant-id", "t2", "--tenant", "acme", "--package", str(self.archive)
        )

        self.assertEqual(1, code)
        self.assertIn("belongs to tenant t1", err)

    def test_setup_installs_the_command_and_a_rerun_keeps_the_clone_protocol(self) -> None:
        self.setup_tenant("acme", "t1")
        self.assertTrue((self.home / ".local" / "bin" / "slop-haus").is_file())
        config_file = self.root("acme") / "supervisor.toml"
        cfg.write(config_file, cfg.load(config_file).with_clone_protocol("ssh"))

        self.setup_tenant("acme", "t1")

        self.assertEqual("ssh", cfg.load(config_file).clone_protocol)

    def test_with_two_roots_a_command_refuses_until_use_picks_one(self) -> None:
        self.setup_tenant("acme", "t1")
        self.setup_tenant("globex", "t2")

        code, _, err = self.run_cli("secret", "list")
        self.assertEqual(1, code)
        self.assertIn("acme", err)
        self.assertIn("globex", err)

        self.assertEqual(0, self.run_cli("use", "globex")[0])
        code, out, _ = self.run_cli("secret", "list")
        self.assertEqual((0, "CLAUDE_CODE_OAUTH_TOKEN (empty)\n"), (code, out))

    def test_use_refuses_a_tenant_that_is_not_set_up(self) -> None:
        self.setup_tenant("acme", "t1")

        code, _, err = self.run_cli("use", "ghost")

        self.assertEqual(1, code)
        self.assertIn("acme", err)

    def test_status_covers_every_tenant_running_or_not(self) -> None:
        self.setup_tenant("acme", "t1")
        self.setup_tenant("globex", "t2")
        with (self.root("globex") / ".run.lock").open("w") as held:
            held.write("4242\n")
            held.flush()
            fcntl.flock(held, fcntl.LOCK_EX)
            code, out, _ = self.run_cli("status")

        self.assertEqual(0, code)
        self.assertIn("acme: not running", out)
        self.assertIn("globex: running (pid 4242)", out)
        self.assertEqual(2, out.count("slots: none"))

    def test_status_for_one_tenant_lists_only_it(self) -> None:
        self.setup_tenant("acme", "t1")
        self.setup_tenant("globex", "t2")

        _, out, _ = self.run_cli("status", "-t", "globex")

        self.assertNotIn("acme", out)
        self.assertIn("globex", out)


class RunLogFileTest(CliRootsTest):
    """`run` hands serve() the log path; serve itself is replaced, so nothing starts."""

    def serve_options(self, *extra: str) -> tuple[int, object]:
        self.setup_tenant("acme", "t1")
        seen: list[object] = []

        def fake_serve(_layout: object, options: object) -> int:
            seen.append(options)
            return 0

        original = cli.serve
        cli.serve = fake_serve
        self.addCleanup(setattr, cli, "serve", original)
        code, _, _ = self.run_cli("run", "-t", "acme", *extra)
        return code, seen[0]

    def test_the_log_goes_to_the_roots_supervisor_log_by_default(self) -> None:
        code, options = self.serve_options()

        self.assertEqual(0, code)
        self.assertEqual(self.root("acme") / "supervisor.log", getattr(options, "log_file"))

    def test_no_log_file_writes_none(self) -> None:
        _, options = self.serve_options("--no-log-file")

        self.assertIsNone(getattr(options, "log_file"))

    def test_an_explicit_path_wins(self) -> None:
        _, options = self.serve_options("--log-file", "/elsewhere/x.log")

        self.assertEqual(Path("/elsewhere/x.log"), getattr(options, "log_file"))

    def test_both_flags_are_refused(self) -> None:
        self.setup_tenant("acme", "t1")

        code, _, err = self.run_cli("run", "-t", "acme", "--log-file", "x.log", "--no-log-file")

        self.assertEqual(78, code)
        self.assertIn("contradict", err)
