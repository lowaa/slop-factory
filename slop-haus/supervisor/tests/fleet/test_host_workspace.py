"""A Remote Control host on its real workspace: `ProvisionedHost` over a `Provisioned`, not a fake of it.

The update script stands in for the server's: it lays down the token helper and the workspace's own update.sh (which
the start's refresh runs), and like sync-repos clones a missing repo only over the protocol it is handed, reporting a
failed clone on stderr and carrying on.
"""

from __future__ import annotations

import contextlib
import io
import logging
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from fakes import FakeAlerts, FakeClock
from slop_haus_supervisor.fleet.host_slot import RemoteControlDeps, RemoteControlSlot
from slop_haus_supervisor.fleet.host_slots import ProvisionedHost
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.clone_probe import ProbeResult
from slop_haus_supervisor.workspace.layout import Layout, clone_protocol_chooser
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from slop_haus_supervisor.workspace.update_script import UpdateScript
from test_host_slot import CONFIG, FakeSpawner

GIT_REFUSAL = "fatal: could not read Username for 'https://git.example': terminal prompts disabled"
SYNC = f"""#!/usr/bin/env bash
[ -e app ] && exit 0
if [ "${{SLOP_HAUS_CLONE_PROTOCOL:-}}" = ssh ]; then
  git init -q app && git -C app remote add origin git@git.example:acme/app.git
else
  echo "{GIT_REFUSAL}" >&2
  echo "slop-haus: could not clone app from git.example/acme/app -- check this machine's git access to it" >&2
fi
"""
SCRIPT = UpdateScript(
    f"""mkdir -p .slop-haus
printf '#!/bin/sh\\n' > .slop-haus/token.sh
cat > .slop-haus/update.sh <<'SYNC'
{SYNC}SYNC
chmod +x .slop-haus/update.sh
.slop-haus/update.sh
""",
    (("app", "git.example/acme/app"),),
)


class FakeEnrollment:
    server = "https://example.test"

    def issue(self) -> TokenPair:
        return TokenPair("access", "refresh", 9999999999)

    def update_script(self, tokens: TokenPair) -> str:
        return SCRIPT

    def refresh(self, tokens: TokenPair) -> TokenPair:
        return TokenPair("access-2", "refresh-2", 9999999999)

    def revoke(self, tokens: TokenPair) -> None:
        raise AssertionError("a live credential is kept, not replaced")


class HostOnItsWorkspaceTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.spawner = FakeSpawner()
        self.probed: list[str] = []
        self.ssh_works = False
        env = mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""})
        env.start()
        self.addCleanup(env.stop)
        self.store_protocol("ssh")

    def store_protocol(self, protocol: str) -> None:
        """The machine's choice, as a slot's probe or setup stored it; "" for none yet."""
        self.layout.config_file.write_text(cfg.SupervisorConfig("https://example.test", "t", "acme").with_clone_protocol(protocol).dumps())

    def probe(self, url: str) -> ProbeResult:
        self.probed.append(url)
        return ProbeResult(self.ssh_works, "test")

    def slot(self) -> RemoteControlSlot:
        directory = self.layout.remote_control_workspace("rc1")
        unit = Provisioned(directory, Kind.REMOTE_CONTROL, SubprocessRunner(), logging.getLogger("test"), FakeEnrollment())
        slot = RemoteControlSlot("rc1", "claude", RemoteControlDeps(
            self.layout, ProvisionedHost(unit, clone_protocol_chooser(self.layout, ssh_works=self.probe)), self.spawner, FakeClock(), FakeAlerts(),
            lambda _harness, _secrets: "", {"PATH": "/usr/bin"}, logging.getLogger("test"), config=CONFIG,
        ))
        self.addCleanup(slot.abandon_run)
        return slot

    def test_a_new_workspace_is_signed_in_cloned_and_the_host_started_in_it(self) -> None:
        slot = self.slot()

        slot.step()

        directory = self.layout.remote_control_workspace("rc1")
        self.assertEqual(Phase.RUNNING, slot.phase)
        argv = self.spawner.spawned[0][0]
        self.assertEqual(str(directory), argv[argv.index("--workspace") + 1])
        self.assertTrue((directory / "app" / ".git").is_dir())
        self.assertEqual([], self.probed, "the stored protocol settles it")

    def test_on_a_machine_with_no_choice_yet_the_probe_picks_the_protocol_and_it_is_stored(self) -> None:
        self.store_protocol("")
        self.ssh_works = True
        slot = self.slot()

        with contextlib.redirect_stdout(io.StringIO()):
            slot.step()

        self.assertEqual(Phase.RUNNING, slot.phase)
        self.assertTrue((self.layout.remote_control_workspace("rc1") / "app" / ".git").is_dir())
        self.assertEqual(["git@git.example:acme/app.git"], self.probed)
        self.assertEqual("ssh", cfg.load(self.layout.config_file).clone_protocol)

    def test_a_host_is_never_started_without_its_repos(self) -> None:
        self.store_protocol("https")
        slot = self.slot()
        for _ in range(2):
            with self.assertRaises(ConfigError):
                slot.step()

        self.assertEqual([], self.spawner.spawned)
        self.assertFalse((self.layout.remote_control_workspace("rc1") / "app").exists())

    def test_a_failed_clone_names_gits_reason_first_in_the_error_and_on_the_board(self) -> None:
        self.store_protocol("https")
        slot = self.slot()

        with self.assertRaises(ConfigError) as raised:
            slot.step()

        self.assertTrue(str(raised.exception).startswith(GIT_REFUSAL), str(raised.exception))
        self.assertIn("is not a checkout of git.example/acme/app", str(raised.exception))
        snapshot = slot.snapshot()
        self.assertEqual(Reason.BACKOFF_AFTER_ERROR.value, snapshot.reason)
        self.assertTrue(snapshot.reason_detail.startswith(GIT_REFUSAL), snapshot.reason_detail)
        self.assertEqual([], self.spawner.spawned)

    def test_a_clone_that_failed_at_sign_in_is_made_at_a_later_start_with_the_machines_protocol(self) -> None:
        self.store_protocol("https")
        slot = self.slot()
        with self.assertRaises(ConfigError):
            slot.step()
        # Fixed since, e.g. by `slop-haus` setup or a slot's clone: the next start is to use it.
        self.store_protocol("ssh")

        slot.step()

        self.assertTrue((self.layout.remote_control_workspace("rc1") / "app" / ".git").is_dir())
        self.assertEqual(Phase.RUNNING, slot.phase)


if __name__ == "__main__":
    unittest.main()
