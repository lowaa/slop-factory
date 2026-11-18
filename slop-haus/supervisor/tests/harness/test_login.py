from __future__ import annotations

import fcntl
import os
import stat
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, build_harness
from slop_haus_supervisor.harness.login import HarnessLogins, LoginError, TerminalCommands
from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.envfile import EnvFile
from slop_haus_supervisor.workspace.layout import Layout

TOKEN = "sk-ant-oat01-SUPERSECRETTOKENVALUE0123"
UID = 1000

# Each fake records how it was run in $FAKE_DIR/<name>.log; FAKE_OK makes it log in.
CODEX = """#!/bin/sh
{ echo "home=$CODEX_HOME"; echo "mode=$(stat -c %a "$CODEX_HOME")"; echo "argv=$*"; } > "$FAKE_DIR/codex.log"
[ -n "$FAKE_OK" ] && printf '{"auth_mode":"chatgpt","tokens":{"id_token":"x"}}' > "$CODEX_HOME/auth.json"
exit 0
"""
PI = """#!/bin/sh
{ echo "dir=$PI_CODING_AGENT_DIR"; echo "mode=$(stat -c %a "$PI_CODING_AGENT_DIR")"; } > "$FAKE_DIR/pi.log"
[ -n "$FAKE_OK" ] && printf '{"anthropic":{}}' > "$PI_CODING_AGENT_DIR/auth.json"
exit 0
"""
CLAUDE = f"""#!/bin/sh
echo "Open the link, then paste the code."
[ -n "$FAKE_OK" ] && echo "Your token: {TOKEN}"
[ -n "$FAKE_OK" ] || exit 1
exit 0
"""


class FakeHidden:
    def __init__(self, answer: str = "") -> None:
        self.answer = answer
        self.asked: list[str] = []

    def __call__(self, question: str) -> str:
        self.asked.append(question)
        return self.answer


class HarnessLoginTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name) / "root"
        self.bin = Path(tmp.name) / "bin"
        self.fake_dir = Path(tmp.name) / "fake"
        for d in (self.root, self.bin, self.fake_dir):
            d.mkdir()
        self.layout = Layout(self.root)
        self.config = cfg.SupervisorConfig("https://s", "t", "acme", "codex", slots=("a",))
        cfg.write(self.layout.config_file, self.config)
        self.out: list[str] = []
        self.hidden = FakeHidden()
        self.ok = True

    def fake(self, name: str, script: str) -> None:
        path = self.bin / name
        path.write_text(script)
        path.chmod(0o755)

    def logins(self, euid: int = UID, owner: int = UID) -> HarnessLogins:
        environ = {"PATH": str(self.bin) + ":/usr/bin:/bin", "FAKE_DIR": str(self.fake_dir), **({"FAKE_OK": "1"} if self.ok else {})}

        def build(name: str):
            secrets = {k: v for k, v in EnvFile(self.layout.secrets_file).values().items() if v}
            context = HarnessContext(self.root, "https://s", secrets, pi_extension=self.root / "pi.ts")
            return build_harness(HarnessKind.SLOT, name, context)

        return HarnessLogins(self.layout, build, TerminalCommands(), self.hidden, self.out.append, environ, euid, owner)

    def log(self, name: str) -> dict[str, str]:
        return dict(line.split("=", 1) for line in (self.fake_dir / f"{name}.log").read_text().splitlines())

    def test_codex_runs_in_a_private_folder_with_the_device_code_option(self) -> None:
        self.fake("codex", CODEX)

        outcome = self.logins().login("codex")

        home = self.root / "harness" / "codex"
        self.assertEqual({"home": str(home), "mode": "700", "argv": "login --device-auth"}, self.log("codex"))
        self.assertTrue(outcome.ok)
        self.assertEqual(("codex is logged in",), outcome.lines)

    def test_an_existing_folder_is_made_private(self) -> None:
        self.fake("pi", PI)
        (self.root / "harness" / "pi").mkdir(parents=True, mode=0o755)

        self.logins().login("pi")

        self.assertEqual("700", self.log("pi")["mode"])
        self.assertEqual(str(self.root / "harness" / "pi"), self.log("pi")["dir"])

    def test_a_broken_login_leaves_the_harness_missing(self) -> None:
        self.ok = False
        self.fake("codex", CODEX)

        outcome = self.logins().login("codex")

        self.assertFalse(outcome.ok)
        self.assertIn("still not logged in", outcome.lines[0])
        self.assertIn("MISSING", self.logins().status_lines()[1 + 2])

    def test_claude_stores_the_token_without_showing_it(self) -> None:
        self.fake("claude", CLAUDE)

        outcome = self.logins().login("claude")

        self.assertTrue(outcome.ok)
        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])
        self.assertNotIn(TOKEN, "\n".join([*self.out, *outcome.lines]))
        self.assertIn("Open the link", "\n".join(self.out))
        self.assertEqual([], self.hidden.asked)

    def test_claude_without_a_token_in_its_output_asks_for_it_hidden(self) -> None:
        self.fake("claude", "#!/bin/sh\necho 'nothing here'\n")
        self.hidden.answer = TOKEN

        self.assertTrue(self.logins().login("claude").ok)

        self.assertEqual(1, len(self.hidden.asked))
        self.assertEqual(TOKEN, EnvFile(self.layout.secrets_file).values()["CLAUDE_CODE_OAUTH_TOKEN"])

    def test_a_failed_claude_login_stores_nothing(self) -> None:
        self.ok = False
        self.fake("claude", CLAUDE)

        outcome = self.logins().login("claude")

        self.assertFalse(outcome.ok)
        self.assertFalse(self.layout.secrets_file.exists() and "CLAUDE_CODE_OAUTH_TOKEN" in EnvFile(self.layout.secrets_file).values())

    def test_root_and_another_users_root_are_refused(self) -> None:
        self.fake("codex", CODEX)

        with self.assertRaisesRegex(LoginError, "as root"):
            self.logins(euid=0).login("codex")
        with self.assertRaisesRegex(LoginError, "another user"):
            self.logins(euid=UID + 1).login("codex")
        self.assertFalse((self.root / "harness" / "codex").exists())

    def test_a_missing_binary_says_how_to_install_it_and_where_it_looked(self) -> None:
        with self.assertRaises(LoginError) as raised:
            self.logins().login("codex")
        message = str(raised.exception)
        self.assertRegex(message, "codex was not found on PATH")
        entry = registry.find("codex")
        assert entry is not None
        self.assertIn(entry.install_fix, message)
        self.assertIn(f"searched: {self.bin}:/usr/bin:/bin", message)
        self.assertIn(f"{self.root / 'run.env'}, not ~/.bashrc", message)

    def test_status_names_the_used_harness_and_each_state(self) -> None:
        self.fake("codex", CODEX)
        self.logins().login("codex")

        lines = self.logins().status_lines()

        self.assertEqual("  codex: logged in", lines[3])
        self.assertTrue(lines[1].startswith("  claude: MISSING") and lines[1].endswith("(not used by any slot)"))
        self.assertTrue(lines[2].startswith("  pi: MISSING"))

    def test_a_running_supervisor_is_told_to_restart_and_paused_slots_listed(self) -> None:
        self.fake("claude", CLAUDE)
        cfg.write(self.layout.config_file, cfg.SupervisorConfig("https://s", "t", "acme", "claude", slots=("a",)))
        from slop_haus_supervisor.fleet import login_failure

        paused_state.write(self.layout.paused_file, [paused_state.PausedSlot("a", login_failure.SUBJECT + ". x", 0.0)])
        with open(self.root / ".run.lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            outcome = self.logins().login("claude")

        text = "\n".join(outcome.lines)
        self.assertIn("slop-haus stop", text)
        self.assertIn("slots paused on the login: a", text)

    def test_a_stopped_supervisor_needs_no_follow_up(self) -> None:
        self.fake("codex", CODEX)
        self.assertEqual(("codex is logged in",), self.logins().login("codex").lines)
        self.assertEqual(0o700, stat.S_IMODE(os.stat(self.root / "harness" / "codex").st_mode))

    def test_a_folder_login_that_exits_non_zero_fails_even_if_a_file_was_left(self) -> None:
        self.fake("pi", PI.replace("exit 0", "exit 3"))

        outcome = self.logins().login("pi")

        self.assertFalse(outcome.ok)
        self.assertIn("exited with 3", outcome.lines[0])

    def test_a_running_supervisor_is_not_told_to_restart_for_a_folder_login(self) -> None:
        self.fake("codex", CODEX)
        with open(self.root / ".run.lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            outcome = self.logins().login("codex")

        self.assertEqual(("codex is logged in",), outcome.lines)
