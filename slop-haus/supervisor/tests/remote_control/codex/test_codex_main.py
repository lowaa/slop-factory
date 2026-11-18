from __future__ import annotations

import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.remote_control.host import main as main_module
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex
from slop_haus_supervisor.remote_control.host.codex_records import CodexRecords


class CodexMainTest(unittest.TestCase):
    """main's choice of harness."""

    def test_codex_is_served_with_its_harness_and_records(self) -> None:
        workspace = Path(tempfile.mkdtemp(prefix="remote-control-codex-main-"))
        self.addCleanup(shutil.rmtree, workspace, ignore_errors=True)
        args = main_module.argparse.Namespace(harness="codex", provider="", model="", thinking="", supervised=False)
        chosen = main_module.choose_harness(args, workspace, workspace / "state", "https://s.example", workspace / "t.sh", main_module.Settings())
        self.assertIsInstance(chosen.harness, RemoteControlCodex)
        assert chosen.records is not None
        records = chosen.records(mock.MagicMock())
        self.assertIsInstance(records, CodexRecords)
        self.assertFalse(records.retries_session)
        with mock.patch.dict(os.environ, {"PATH": ""}), self.assertRaises(FileNotFoundError):
            chosen.harness.check_installed()
