from __future__ import annotations

import contextlib
import io
from unittest import mock

from slop_haus_supervisor import cli
from slop_haus_supervisor.service.installer import InstallOptions
from support import TempDirTestCase, make_archive


class ServiceCliTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.run_cli("init", "--server", "https://s", "--tenant-id", "t1", "--default-tenant", "acme", "--package", str(make_archive(self.tmp / "p.tar.gz")))

    def run_cli(self, *argv: str) -> int:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return cli.main(list(argv), {"PATH": "/usr/bin"}, self.home, self.tmp)

    def test_install_hands_its_flags_to_the_installer(self) -> None:
        with mock.patch.object(cli, "ServiceInstaller") as installer, mock.patch.object(cli, "_service_manager"):
            installer.return_value.install.return_value = 7

            code = self.run_cli("service", "install", "--tenant", "acme", "--when-idle", "--idle-deadline", "9", "--yes")

        self.assertEqual(7, code)
        installer.return_value.install.assert_called_once_with(InstallOptions(when_idle=True, yes=True, idle_deadline=9.0))

    def test_uninstall_is_dispatched(self) -> None:
        with mock.patch.object(cli, "ServiceInstaller") as installer, mock.patch.object(cli, "_service_manager"):
            installer.return_value.uninstall.return_value = 0

            self.assertEqual(0, self.run_cli("service", "uninstall", "--tenant", "acme"))

        installer.return_value.uninstall.assert_called_once_with()
