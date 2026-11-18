"""The cases of run_remote_control.bats that pin `load_env_file`'s grammar, run against the Python loader."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.service.run_env import LauncherEnv, LauncherEnvError


class LauncherEnvTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name) / "workspace"
        (self.root / ".slop-haus").mkdir(parents=True)
        self.outside = Path(tmp.name) / "elsewhere.env"
        self.run_env = self.root / ".slop-haus" / "run.env"
        self.warnings: list[str] = []

    def load(self, text: str | None = None) -> dict[str, str]:
        if text is not None:
            self.run_env.write_text(text)
        return LauncherEnv(self.root, self.warnings.append).load(self.run_env)

    def refused(self, text: str) -> str:
        with self.assertRaises(LauncherEnvError) as raised:
            self.load(text)
        return str(raised.exception)

    def test_no_run_env_sets_nothing(self) -> None:
        self.assertEqual({}, self.load())

    def test_values_have_quotes_stripped_and_nothing_expanded(self) -> None:
        values = self.load(
            "# a comment\nPLAIN=one\nexport EXPORTED=two\nDOUBLE=\"three four\"\nSINGLE='$HOME stays literal'\nEMPTY=\n"
        )
        self.assertEqual(
            {"PLAIN": "one", "EXPORTED": "two", "DOUBLE": "three four", "SINGLE": "$HOME stays literal", "EMPTY": ""}, values
        )

    def test_a_trailing_comment_and_escapes_are_part_of_the_value(self) -> None:
        self.assertEqual({"A": "x # y", "B": "a\\nb"}, self.load('A=x # y\nB="a\\nb"\n'))

    def test_a_file_without_a_final_newline_keeps_its_last_line(self) -> None:
        self.assertEqual({"A": "1", "B": "2"}, self.load("A=1\nB=2"))

    def test_a_line_that_is_not_an_assignment_is_an_error_never_executed(self) -> None:
        self.assertIn("run.env:1: expected NAME=value", self.refused('touch "$STATE/pwned"\n'))

    def test_an_unmatched_quote_is_an_error_not_a_trimmed_value(self) -> None:
        for bad in ('KEY="abc', "KEY='abc", 'KEY=abc"', 'KEY="', "KEY=\"a'"):
            self.assertIn("unmatched quote in KEY", self.refused(bad + "\n"), bad)

    def test_run_env_itself_may_set_labels_but_no_other_slop_haus_name(self) -> None:
        values = self.load('SLOP_HAUS_LABELS="gpu=a100,docker"\nSLOP_HAUS_URL=http://x\nSLOP_HAUS_HARNESS=pi\nSLOP_HAUS_LABELSX=nope\n')
        self.assertEqual({"SLOP_HAUS_LABELS": "gpu=a100,docker"}, values)
        for name in ("SLOP_HAUS_URL", "SLOP_HAUS_HARNESS", "SLOP_HAUS_LABELSX"):
            self.assertTrue(any(f"ignoring {name}" in w for w in self.warnings), name)
        self.assertFalse(any("ignoring SLOP_HAUS_LABELS " in w for w in self.warnings))

    def test_labels_are_ignored_in_an_included_file_even_when_asked_for(self) -> None:
        (self.root / ".env").write_text("SLOP_HAUS_LABELS=from-the-repo\n")
        self.assertEqual({}, self.load("< .env SLOP_HAUS_LABELS\n"))
        self.assertIn("ignoring SLOP_HAUS_LABELS", self.warnings[0])

    def test_run_env_may_set_token_impl_but_an_include_may_not(self) -> None:
        self.assertEqual({"SLOP_HAUS_TOKEN_IMPL": "python"}, self.load("SLOP_HAUS_TOKEN_IMPL=python\n"))
        (self.root / ".env").write_text("SLOP_HAUS_TOKEN_IMPL=python\n")
        self.warnings.clear()
        self.assertEqual({}, self.load("< .env SLOP_HAUS_TOKEN_IMPL\n"))
        self.assertIn("ignoring SLOP_HAUS_TOKEN_IMPL", self.warnings[0])

    def test_run_env_may_set_the_codex_shared_login_flag_but_an_include_may_not(self) -> None:
        self.assertEqual({"SLOP_HAUS_CODEX_SHARED_LOGIN": "verified"}, self.load("SLOP_HAUS_CODEX_SHARED_LOGIN=verified\n"))
        (self.root / ".env").write_text("SLOP_HAUS_CODEX_SHARED_LOGIN=verified\n")
        self.warnings.clear()
        self.assertEqual({}, self.load("< .env SLOP_HAUS_CODEX_SHARED_LOGIN\n"))
        self.assertIn("ignoring SLOP_HAUS_CODEX_SHARED_LOGIN", self.warnings[0])

    def test_variables_that_change_what_shells_run_are_refused_in_run_env_and_by_include(self) -> None:
        for name in ("BASH_ENV", "PATH", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "BASH_FUNC_x", "PYTHONPATH", "NODE_OPTIONS"):
            with self.subTest(name=name, where="run.env"):
                self.assertIn(f"refusing {name}", self.refused(f"{name}=/x\n"))
            with self.subTest(name=name, where="include"):
                (self.root / ".env").write_text(f"{name}=/x\n")
                self.assertIn(f"refusing {name}", self.refused(f"< .env {name}\n"))

    def test_an_include_supplies_only_the_names_asked_for_and_ignores_the_rest(self) -> None:
        (self.root / ".env").write_text(
            "# repo secrets\nWANTED=yes\nBASH_ENV=/payload.sh\nGIT_SSH_COMMAND=touch pwned\nUNWANTED=no\n"
            "this line is not NAME=value at all\nMULTI=\"unterminated\n"
        )
        self.assertEqual({"WANTED": "yes"}, self.load("< .env WANTED\n"))

    def test_an_include_is_read_not_executed(self) -> None:
        (self.root / ".env").write_text('SNEAKY=$(touch "$STATE/pwned")\n')
        self.assertEqual({"SNEAKY": '$(touch "$STATE/pwned")'}, self.load("< .env SNEAKY\n"))

    def test_an_include_hands_over_the_repos_env_file_beside_run_env_values(self) -> None:
        (self.root / ".env").write_text("# repo secrets\nOPENROUTER_API_KEY=sk-test\n")
        self.assertEqual(
            {"OPENROUTER_API_KEY": "sk-test", "LOCAL": "from-run-env"}, self.load("< .env OPENROUTER_API_KEY\nLOCAL=from-run-env\n")
        )

    def test_an_include_has_to_name_what_it_takes(self) -> None:
        self.assertIn("name the variables to take", self.refused("< .env\n"))

    def test_a_missing_include_is_skipped_with_a_warning(self) -> None:
        self.assertEqual({}, self.load("< .env KEY\n"))
        self.assertIn(".env does not exist", self.warnings[0])

    def test_an_include_outside_the_workspace_is_refused_also_through_a_link(self) -> None:
        self.outside.write_text("SECRET=outside\n")
        self.assertIn("outside this workspace", self.refused("< ../elsewhere.env SECRET\n"))
        (self.root / "linked.env").symlink_to(self.outside)
        self.assertIn("outside this workspace", self.refused("< linked.env SECRET\n"))

    def test_a_run_env_that_links_outside_the_workspace_is_refused(self) -> None:
        self.outside.write_text("SECRET=outside\n")
        self.run_env.symlink_to(self.outside)
        with self.assertRaises(LauncherEnvError) as raised:
            self.load()
        self.assertIn("leads outside this workspace", str(raised.exception))

    def test_includes_do_not_nest(self) -> None:
        (self.root / ".env").write_text("< other.env KEY\n")
        (self.root / "other.env").write_text("KEY=nested\n")
        self.assertEqual({}, self.load("< .env KEY\n"))

    def test_a_refused_line_after_good_ones_returns_nothing_to_apply(self) -> None:
        with self.assertRaises(LauncherEnvError):
            self.load("GOOD=1\nBASH_ENV=/x\n")


if __name__ == "__main__":
    unittest.main()
