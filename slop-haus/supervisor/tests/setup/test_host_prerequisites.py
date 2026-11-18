from __future__ import annotations

import tempfile
import unittest
from collections.abc import Callable
from pathlib import Path

from fakes import FakeRunner
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.setup.prerequisites import LOW_DISK_BYTES, Finding, Prerequisites


def git_config_answers(**exit_codes: int) -> Callable[[list[str]], ProcessResult]:
    """Each tool exits with its code (default 0); `git config` also prints a value when it succeeds."""

    def answer(argv: list[str]) -> ProcessResult:
        tool = Path(argv[0]).name
        code = exit_codes.get(tool, 0)
        return ProcessResult(code, "store\n" if [tool, argv[1]] == ["git", "config"] and code == 0 else "", "")

    return answer


class PrerequisitesTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name)
        self.tools = {"uv": "/x/uv", "claude": "/x/claude", "git": "/x/git", "docker": "/x/docker", "gh": "/x/gh"}
        # What the service finds: by default what the shell does.
        self.service_tools = self.tools
        self.env = {"SSH_AUTH_SOCK": "/sock"}
        self.free = LOW_DISK_BYTES * 2
        self.runner = FakeRunner(git_config_answers())

    def findings(self, harness: str = "claude") -> dict[str, Finding]:
        checks = Prerequisites(
            self.runner, harness, self.env, self.home, self.home, self.tools.get, self.service_tools.get, lambda _p: self.free
        )
        return {f.name: f for f in checks.check()}

    def test_a_ready_host_passes_everything(self) -> None:
        self.assertTrue(all(f.ok for f in self.findings().values()))

    def test_each_missing_tool_is_named_with_its_fix(self) -> None:
        for tool, name in (("uv", "uv"), ("claude", "claude"), ("docker", "docker"), ("git", "git")):
            del self.tools[tool]
            found = self.findings()[name]
            self.assertFalse(found.ok, tool)
            self.assertTrue(found.blocking)
            self.assertTrue(found.detail, tool)
            self.tools[tool] = "/x"

    def test_a_tool_only_the_shell_finds_is_reported_with_the_directory_to_add(self) -> None:
        self.tools["claude"] = "/home/me/.local/bin/claude"
        self.service_tools = {k: v for k, v in self.tools.items() if k != "claude"}

        found = self.findings()["claude"]

        self.assertFalse(found.ok)
        self.assertTrue(found.blocking)
        self.assertEqual("/home/me/.local/bin", found.path_fix)
        self.assertIn("/home/me/.local/bin/claude", found.detail)

    def test_a_tool_the_service_finds_passes_even_if_the_shell_does_not(self) -> None:
        self.service_tools = dict(self.tools)
        self.tools = {}

        self.assertTrue(self.findings()["claude"].ok)

    def test_a_tool_on_neither_path_keeps_its_install_fix(self) -> None:
        self.service_tools = {}
        self.tools = {}

        found = self.findings()["claude"]

        self.assertFalse(found.ok)
        self.assertEqual("", found.path_fix)

    def test_a_non_default_harness_only_the_shell_finds_is_reported_with_the_folder_to_add(self) -> None:
        self.service_tools = dict(self.tools)
        self.tools["pi"] = "/h/pi-node/bin/pi"

        found = self.findings()["pi"]

        self.assertFalse(found.ok)
        self.assertFalse(found.blocking)
        self.assertEqual("/h/pi-node/bin", found.path_fix)

    def test_a_non_default_harness_the_shell_lacks_is_not_asked_for(self) -> None:
        self.assertNotIn("pi", self.findings())
        self.assertNotIn("codex", self.findings())

    def test_a_non_default_harness_the_service_finds_passes(self) -> None:
        self.tools["pi"] = "/x/pi"

        self.assertTrue(self.findings()["pi"].ok)

    def test_a_missing_gh_warns_and_does_not_block(self) -> None:
        del self.tools["gh"]

        found = self.findings()["gh"]

        self.assertFalse(found.ok)
        self.assertFalse(found.blocking)

    def test_probes_run_the_executable_the_service_finds(self) -> None:
        self.tools = {}
        self.env = {}
        self.service_tools = {"git": "/svc/git", "docker": "/svc/docker"}
        self.findings()

        self.assertIn(["/svc/docker", "info"], self.runner.calls)
        self.assertIn(["/svc/git", "config", "--get-all", "credential.helper"], self.runner.calls)

    def test_pi_hosts_need_pi_not_claude(self) -> None:
        del self.tools["claude"]

        self.assertNotIn("claude", self.findings("pi"))
        self.assertFalse(self.findings("pi")["pi"].ok)

    def test_docker_that_is_installed_but_unreachable_fails(self) -> None:
        self.runner = FakeRunner(git_config_answers(docker=1))

        self.assertIn("not reachable", self.findings()["docker"].detail)

    def test_git_push_needs_a_key_an_agent_or_a_credential_helper(self) -> None:
        self.env = {}
        self.runner = FakeRunner(git_config_answers(git=1))
        self.assertFalse(self.findings()["git push"].ok)

        (self.home / ".ssh").mkdir()
        (self.home / ".ssh" / "id_ed25519.pub").write_text("pub")
        self.assertFalse(self.findings()["git push"].ok)

        (self.home / ".ssh" / "id_ed25519").write_text("key")
        self.assertTrue(self.findings()["git push"].ok)

        (self.home / ".ssh" / "id_ed25519").unlink()
        self.runner = FakeRunner(git_config_answers(git=0))
        self.assertTrue(self.findings()["git push"].ok)

    def test_low_disk_warns_without_blocking(self) -> None:
        self.free = LOW_DISK_BYTES // 2

        found = self.findings()["disk"]

        self.assertFalse(found.ok)
        self.assertFalse(found.blocking)
