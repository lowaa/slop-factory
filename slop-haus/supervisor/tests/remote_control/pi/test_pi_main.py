from __future__ import annotations

import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.remote_control.host import main as main_module
from slop_haus_supervisor.remote_control.pi.harness import RemoteControlPi
from slop_haus_supervisor.remote_control.pi.options import pi_extension, pi_variant
from slop_haus_supervisor.remote_control.pi.rpc import PiRpc
from slop_haus_supervisor.remote_control.wire.errors import Refused

SERVER = "https://slop-haus.example"


class PiMainTest(unittest.TestCase):
    """main's choice of harness."""

    def setUp(self) -> None:
        self.workspace = Path(tempfile.mkdtemp(prefix="remote-control-pi-main-"))
        self.addCleanup(shutil.rmtree, self.workspace, ignore_errors=True)
        (self.workspace / ".slop-haus" / "pi").mkdir(parents=True)
        (self.workspace / ".slop-haus" / "pi" / "slop-haus.ts").write_text("")
        env = mock.patch.dict(os.environ, {"SLOP_HAUS_URL": SERVER})
        env.start()
        self.addCleanup(env.stop)
        self.mocks: dict[str, mock.MagicMock] = {}
        for name in ("Host", "Poller", "Reporter", "Client", "SlotToken", "HostLease"):
            patcher = mock.patch.object(main_module, name)
            self.mocks[name] = patcher.start()
            self.addCleanup(patcher.stop)
        signals = mock.patch.object(main_module.signal, "signal")
        signals.start()
        self.addCleanup(signals.stop)
        self.which = mock.patch("slop_haus_supervisor.harness.pi.shutil.which", return_value="/bin/pi")
        self.which.start()
        self.addCleanup(self.which.stop)

    def run_main(self, *args: str) -> int:
        return main_module.main(["--workspace", str(self.workspace), "--harness", "pi", *args])

    def test_pi_starts_the_host_with_pis_harness_and_records(self) -> None:
        self.assertEqual(0, self.run_main())
        host = self.mocks["Host"]
        self.assertIsInstance(host.call_args.args[2], RemoteControlPi)
        records = host.call_args.kwargs["records"](host.return_value)
        self.assertIsInstance(records, PiRpc)
        self.assertFalse(records.retries_session)

    def test_a_stopped_pi_host_releases_the_remote_control_as_claudes_does(self) -> None:
        self.run_main()
        self.mocks["Client"].return_value.release.assert_called_once()

    def test_pi_must_be_on_the_path(self) -> None:
        self.which.stop()
        with mock.patch("slop_haus_supervisor.harness.pi.shutil.which", return_value=None):
            self.assertEqual(127, self.run_main())
        self.which.start()
        self.mocks["Host"].assert_not_called()

    def test_the_model_flags_reach_pi(self) -> None:
        self.run_main("--provider", "anthropic", "--model", "m", "--thinking", "low")
        harness = self.mocks["Host"].call_args.args[2]
        self.assertEqual("provider=anthropic,model=m,thinking=low", harness._variant)

    def test_a_flag_pi_would_take_for_another_is_refused_before_anything_is_served(self) -> None:
        self.assertEqual(1, self.run_main("--thinking", "loud"))
        self.mocks["Host"].assert_not_called()

    def test_pi_flags_mean_nothing_to_claude(self) -> None:
        for flag in (("--model", "m"), ("--thinking", "low"), ("--provider", "anthropic")):
            with self.subTest(flag), self.assertRaises(SystemExit), mock.patch("sys.stderr"):
                main_module.main(["--workspace", str(self.workspace), *flag])

    def test_a_workspace_without_the_extension_is_refused(self) -> None:
        (self.workspace / ".slop-haus" / "pi" / "slop-haus.ts").unlink()
        self.assertEqual(1, self.run_main())
        self.mocks["Host"].assert_not_called()


class PiOptionsTest(unittest.TestCase):
    def test_a_value_that_could_be_another_flag_is_refused(self) -> None:
        for args in (("--evil", "m", "low"), ("anthropic", "--tools", "low"), ("anthropic", "m", "loud")):
            with self.subTest(args), self.assertRaises(Refused):
                pi_variant(*args)

    def test_the_variant_holds_what_was_chosen(self) -> None:
        self.assertEqual("provider=anthropic,model=m", pi_variant("anthropic", "m", ""))
        self.assertEqual("", pi_variant("", "", ""))

    def test_the_extension_must_be_a_file_that_is_not_a_link(self) -> None:
        workspace = Path(tempfile.mkdtemp(prefix="pi-options-test-"))
        self.addCleanup(shutil.rmtree, workspace, ignore_errors=True)
        with self.assertRaises(Refused):
            pi_extension(workspace)
        extension = workspace / ".slop-haus" / "pi" / "slop-haus.ts"
        extension.parent.mkdir(parents=True)
        extension.write_text("")
        self.assertEqual(extension, pi_extension(workspace))
        extension.unlink()
        extension.symlink_to("/etc/hostname")
        with self.assertRaises(Refused):
            pi_extension(workspace)
