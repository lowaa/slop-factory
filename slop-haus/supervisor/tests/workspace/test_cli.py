from __future__ import annotations

import subprocess

import contextlib
import functools
import unittest.mock
import io
import json
import stat
import sys

from slop_haus_supervisor import cli
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile
from stub_server import StubServer, serving
from support import TempDirTestCase, make_archive

class CliTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        # Not the real working directory, which may itself sit inside a root.
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.archive = make_archive(self.tmp / "package.tar.gz")

    def run_cli(self, *argv: str, env: dict[str, str] | None = None, stdin: str = "") -> tuple[int, str]:
        err = io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            code = cli.main(list(argv), env or {}, self.home, self.cwd)
        return code, err.getvalue()

    def run_secret(self, *argv: str, stdin: str = "") -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with (
            contextlib.redirect_stderr(err),
            contextlib.redirect_stdout(out),
            unittest.mock.patch.object(sys, "stdin", io.StringIO(stdin)),
        ):
            code = cli.main(["secret", *argv, "--tenant", "acme"], {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    def init(self, *extra: str) -> tuple[int, str]:
        return self.run_cli(
            "init", "--server", "https://s.example/", "--tenant-id", "tid", "--tenant", "acme",
            "--package", str(self.archive), *extra,
        )  # fmt: skip

    def test_init_lands_in_the_default_root_and_records_the_tenant_id(self) -> None:
        code, _ = self.init()

        self.assertEqual(0, code)
        loaded = cfg.load(self.home / "slop-haus" / "acme" / "supervisor.toml")
        self.assertEqual(("https://s.example", "tid", "acme"), (loaded.server, loaded.tenant_id, loaded.tenant))

    def test_init_honours_root_over_the_environment(self) -> None:
        target = self.tmp / "elsewhere"

        code, _ = self.run_cli(
            "init", "--root", str(target), "--server", "https://s", "--tenant-id", "tid", "--tenant", "acme",
            "--package", str(self.archive), env={"SLOP_HAUS_HOME": str(self.tmp / "env")},
        )  # fmt: skip

        self.assertEqual(0, code)
        self.assertTrue((target / "supervisor.toml").is_file())
        self.assertFalse((self.tmp / "env").exists())

    def test_commands_after_init_find_the_root_from_the_tenant(self) -> None:
        self.init()

        code, err = self.run_cli("run", "--tenant", "acme")

        self.assertEqual(78, code)
        self.assertIn("no slots", err)

    def test_asking_for_a_tenant_that_was_never_set_up_says_so(self) -> None:
        code, err = self.run_cli("run", "--tenant", "ghost")

        self.assertEqual(78, code)
        self.assertIn("setup script", err)

    def test_no_tenant_and_no_root_is_a_usage_error_not_a_guess(self) -> None:
        code, err = self.run_cli("update")

        self.assertEqual(1, code)
        self.assertIn("--tenant", err)

    def test_update_before_any_slot_has_signed_in_explains_why_it_cannot_authenticate(self) -> None:
        self.init()

        code, err = self.run_cli("update", "--tenant", "acme")

        self.assertEqual(1, code)
        self.assertIn("add one first", err)

    def test_a_tenant_name_that_is_a_path_is_refused(self) -> None:
        code, _ = self.run_cli("run", "--tenant", "../x")

        self.assertEqual(78, code)

    def test_secret_set_reads_the_value_from_stdin_and_never_prints_it(self) -> None:
        self.init()

        code, out, err = self.run_secret("set", "MY_KEY", stdin="s3cret value\n")

        self.assertEqual(0, code)
        self.assertNotIn("s3cret", out + err)
        secrets = self.home / "slop-haus" / "acme" / "secrets.env"
        self.assertEqual(0o600, stat.S_IMODE(secrets.stat().st_mode))
        self.assertEqual({"CLAUDE_CODE_OAUTH_TOKEN": "", "MY_KEY": "s3cret value"}, envfile.read(secrets))

    def test_secret_list_prints_names_only(self) -> None:
        self.init()
        self.run_secret("set", "MY_KEY", stdin="s3cret\n")

        code, out, _ = self.run_secret("list")

        self.assertEqual(0, code)
        self.assertEqual(["CLAUDE_CODE_OAUTH_TOKEN (empty)", "MY_KEY"], out.splitlines())
        self.assertNotIn("s3cret", out)

    def test_secret_unset_removes_the_key(self) -> None:
        self.init()
        self.run_secret("set", "MY_KEY", stdin="value1\n")

        self.assertEqual("removed MY_KEY", self.run_secret("unset", "MY_KEY")[1].strip())
        self.assertEqual("MY_KEY was not set", self.run_secret("unset", "MY_KEY")[1].strip())

    def test_secret_set_refuses_an_empty_value_and_a_bad_name(self) -> None:
        self.init()

        self.assertEqual(1, self.run_secret("set", "MY_KEY", stdin="\n")[0])
        self.assertEqual(1, self.run_secret("set", "MY_KEY", stdin="abc\n")[0])
        code, _, err = self.run_secret("set", "bad key", stdin="value1\n")
        self.assertEqual(1, code)
        self.assertIn("not a variable name", err)

    def test_secret_set_before_setup_does_not_create_a_root(self) -> None:
        code, _, _ = self.run_secret("set", "MY_KEY", stdin="value1\n")

        self.assertEqual(1, code)
        self.assertFalse((self.home / "slop-haus" / "acme").exists())

    def test_secret_commands_before_setup_fail_cleanly(self) -> None:
        for argv in (["unset", "K"], ["list"]):
            with self.subTest(argv=argv):
                code, _, err = self.run_secret(*argv)
                self.assertEqual(1, code)
                self.assertIn("does not exist", err)


SUPERVISOR_ACCESS = "sup-access-secret"
SUPERVISOR_REFRESH = "sup-refresh-secret"
# What the server's update script does for the part that matters here: it leaves token.sh behind.
UPDATE_SCRIPT = f"""#!/usr/bin/env bash
set -euo pipefail
test -f .slop-haus.env
printf '#!/usr/bin/env bash\\nprintf %%s {SUPERVISOR_ACCESS}\\n' > .slop-haus/token.sh
chmod +x .slop-haus/token.sh
"""


class SupervisorSignInTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.stub = StubServer()
        cm = serving(self.stub)
        cm.__enter__()
        self.addCleanup(cm.__exit__, None, None, None)
        archive = make_archive(self.tmp / "package.tar.gz")
        self.root = self.home / "slop-haus" / "acme"
        self.run_cli("init", "--server", self.stub.url, "--tenant-id", "tid", "--tenant", "acme", "--package", str(archive))
        self.stub.on("POST", "/oauth/device_authorization", (200, {
            "device_code": "dev-secret", "user_code": "ABCD", "verification_uri_complete": "https://s.example/device?code=ABCD",
            "expires_in": 600, "interval": 1,
        }))  # fmt: skip
        self.stub.on("POST", "/oauth/token", (200, {"access_token": SUPERVISOR_ACCESS, "refresh_token": SUPERVISOR_REFRESH, "expires_in": 900}))
        self.stub.on("GET", "/api/workspace/update_script", (200, UPDATE_SCRIPT))
        self.board_provisions(False)

    def board_provisions(self, on: bool) -> None:
        self.stub.on("GET", "/api/supervisor/desired_state", (200, {"revision": 0, "agents": [], "provision_from_board": on}))

    def run_cli(self, *argv: str) -> tuple[int, str]:
        out, err = io.StringIO(), io.StringIO()
        no_wait = unittest.mock.patch.object(cli, "device_sign_in", functools.partial(cli.device_sign_in, sleep=lambda _: None))
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out), no_wait:
            code = cli.main([*argv, *(["--tenant", "acme"] if argv[0] != "init" else [])], {}, self.home)
        return code, out.getvalue() + err.getvalue()

    def login(self) -> tuple[int, str]:
        return self.run_cli("login", "--slots", "3")

    def test_login_stores_a_private_credential_and_installs_the_token_helper(self) -> None:
        code, output = self.login()

        self.assertEqual(0, code, output)
        state = self.root / "supervisor" / ".slop-haus"
        self.assertEqual(0o600, stat.S_IMODE((state / "credentials.json").stat().st_mode))
        self.assertEqual(SUPERVISOR_REFRESH, json.loads((state / "credentials.json").read_text())["refresh_token"])
        self.assertTrue((state / "token.sh").is_file())
        self.assertTrue((state / "no-clone").is_file())
        self.assertIn("kind=supervisor&slots=3", output)

    def test_a_failed_token_helper_install_leaves_no_credential_behind(self) -> None:
        self.stub.on("GET", "/api/workspace/update_script", (403, {"detail": "only a worker can do this"}))

        code, output = self.run_cli("login")

        self.assertEqual(1, code)
        self.assertIn("only a worker", output)
        self.assertFalse((self.root / "supervisor").exists())
        self.assertNotIn(SUPERVISOR_ACCESS, output)

    def test_login_refuses_a_slot_count_below_one_before_asking_the_server(self) -> None:
        code, output = self.run_cli("login", "--slots", "0")

        self.assertEqual(1, code)
        self.assertIn("--slots", output)
        self.assertEqual([], self.stub.requests("POST", "/oauth/device_authorization"))

    def test_no_secret_value_reaches_the_output(self) -> None:
        _, output = self.login()

        for secret in (SUPERVISOR_ACCESS, SUPERVISOR_REFRESH, "dev-secret"):
            self.assertNotIn(secret, output)

    def test_a_failed_login_stores_nothing(self) -> None:
        self.stub.on("POST", "/oauth/token", (400, {"error": "access_denied"}))

        code, output = self.run_cli("login")

        self.assertEqual(1, code)
        self.assertIn("denied", output)
        self.assertFalse((self.root / "supervisor").exists())

    def test_slot_add_after_login_creates_the_slot_through_the_supervisor(self) -> None:
        self.login()
        self.stub.on("POST", "/api/supervisor/slots", (201, {"id": "sid", "name": "w1"}))
        self.stub.on("POST", "/api/supervisor/slots/sid/credential", (200, {"access_token": "slot-acc", "refresh_token": "slot-ref", "expires_in": 900}))
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\ntest -f .slop-haus.env\n"))

        code, output = self.run_cli("slot", "add", "w1")

        self.assertEqual(0, code, output)
        self.assertEqual(f"Bearer {SUPERVISOR_ACCESS}", self.stub.requests("POST", "/api/supervisor/slots")[0].auth)
        self.assertEqual(("w1",), cfg.load(self.root / "supervisor.toml").slots)
        self.assertEqual("slot-acc", json.loads((self.root / "slots" / "w1" / ".slop-haus" / "credentials.json").read_text())["access_token"])
        self.assertNotIn("slot-acc", output)

    def test_slot_add_with_the_cap_reached_says_so_and_leaves_nothing(self) -> None:
        self.login()
        self.stub.on("POST", "/api/supervisor/slots", (409, {"detail": "the supervisor is at its slot cap: retire a slot first"}))

        code, output = self.run_cli("slot", "add", "w1")

        self.assertEqual(1, code)
        self.assertIn("slot cap", output)
        self.assertFalse((self.root / "slots" / "w1").exists())

    def test_without_login_slot_add_still_wants_the_setup_script(self) -> None:
        code, output = self.run_cli("slot", "add", "w1")

        self.assertEqual(1, code)
        self.assertIn("--setup-script", output)
        self.assertEqual([], self.stub.requests("POST", "/api/supervisor/slots"))

    def add_w1_while_off(self) -> None:
        self.login()
        self.stub.on("POST", "/api/supervisor/slots", (201, {"id": "sid", "name": "w1"}))
        self.stub.on("POST", "/api/supervisor/slots/sid/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 9}))
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\ntrue\n"))
        self.run_cli("slot", "add", "w1")
        self.stub.on("GET", "/api/supervisor/slots", (200, {"slots": [{"id": "sid", "name": "w1"}], "slot_cap": 3}))
        self.stub.on("DELETE", "/api/supervisor/slots/sid", (200, {}))

    def assert_refused_and_unchanged(self, *argv: str, expect: str) -> None:
        creates = len(self.stub.requests("POST", "/api/supervisor/slots"))
        code, output = self.run_cli(*argv)

        self.assertEqual(1, code, output)
        self.assertIn(expect, output)
        self.assertEqual(creates, len(self.stub.requests("POST", "/api/supervisor/slots")))
        self.assertEqual([], self.stub.requests("DELETE", "/api/supervisor/slots/sid"))

    def test_while_the_board_provisions_slot_add_remove_and_unpause_refuse_and_name_the_board_page(self) -> None:
        self.add_w1_while_off()
        self.board_provisions(True)
        page = f"at {self.stub.url.rstrip('/')}/workers ("

        for argv in (("slot", "add", "w2"), ("slot", "add"), ("slot", "remove", "w1"), ("slot", "unpause", "w1"), ("slot", "unpause", "--all")):
            with self.subTest(argv=argv):
                self.assert_refused_and_unchanged(*argv, expect=page)
        self.assertEqual(("w1",), cfg.load(self.root / "supervisor.toml").slots)
        self.assertTrue((self.root / "slots" / "w1").exists())

    def test_while_the_board_provisions_slot_rebuild_is_not_blocked_by_the_guard(self) -> None:
        self.add_w1_while_off()
        self.board_provisions(True)

        asked = len(self.stub.requests("GET", "/api/supervisor/desired_state"))

        _, output = self.run_cli("slot", "rebuild", "w1")

        self.assertNotIn("provisions agents from the board", output)
        self.assertEqual(asked, len(self.stub.requests("GET", "/api/supervisor/desired_state")))

    def test_when_the_server_cannot_be_asked_slot_commands_refuse_and_change_nothing(self) -> None:
        self.add_w1_while_off()
        self.stub.on("GET", "/api/supervisor/desired_state", (500, {"detail": "boom"}))

        for argv in (("slot", "add", "w2"), ("slot", "remove", "w1"), ("slot", "unpause", "w1")):
            with self.subTest(argv=argv):
                self.assert_refused_and_unchanged(*argv, expect="could not check")
        self.assertTrue((self.root / "slots" / "w1").exists())

    def test_an_unreadable_answer_is_not_read_as_off(self) -> None:
        self.add_w1_while_off()
        self.stub.on("GET", "/api/supervisor/desired_state", (200, "not json"))

        self.assert_refused_and_unchanged("slot", "remove", "w1", expect="could not check")

    def test_an_older_server_that_does_not_say_means_off(self) -> None:
        self.add_w1_while_off()
        self.stub.on("GET", "/api/supervisor/desired_state", (200, {"revision": 0, "agents": []}))

        code, output = self.run_cli("slot", "add", "w2")

        self.assertEqual(0, code, output)

    def test_slot_remove_retires_and_deletes(self) -> None:
        self.login()
        self.stub.on("POST", "/api/supervisor/slots", (201, {"id": "sid", "name": "w1"}))
        self.stub.on("POST", "/api/supervisor/slots/sid/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 9}))
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\ntrue\n"))
        self.run_cli("slot", "add", "w1")
        self.stub.on("GET", "/api/supervisor/slots", (200, {"slots": [{"id": "sid", "name": "w1"}], "slot_cap": 3}))
        self.stub.on("DELETE", "/api/supervisor/slots/sid", (200, {}))
        # The slot's root is no repo; a slot with no clone at all is refused (nothing says it is safe).
        clone = self.root / "slots" / "w1" / "app"
        clone.mkdir()
        subprocess.run(["git", "init", "-q", str(clone)], check=True)
        code, output = self.run_cli("slot", "remove", "w1")

        self.assertEqual(0, code, output)
        self.assertEqual(1, len(self.stub.requests("DELETE", "/api/supervisor/slots/sid")))
        self.assertFalse((self.root / "slots" / "w1").exists())
