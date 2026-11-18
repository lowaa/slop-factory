from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.service import run_env


class RunEnvTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = Path(tmp.name) / "run.env"

    def test_a_missing_file_changes_nothing(self) -> None:
        environ = {"PATH": "/usr/bin"}

        self.assertEqual([], run_env.apply(self.path, environ))
        self.assertEqual({"PATH": "/usr/bin"}, environ)

    def test_path_is_added_in_front_and_other_names_are_set(self) -> None:
        self.path.write_text("PATH=/opt/a:/opt/b\nDOCKER_HOST=unix:///x.sock\n")
        environ = {"PATH": "/usr/bin", "DOCKER_HOST": "old"}

        names = run_env.apply(self.path, environ)

        self.assertEqual(["PATH", "DOCKER_HOST"], names)
        self.assertEqual({"PATH": "/opt/a:/opt/b:/usr/bin", "DOCKER_HOST": "unix:///x.sock"}, environ)

    def test_an_unknown_slop_haus_name_is_dropped_and_a_known_one_is_set(self) -> None:
        self.path.write_text("SLOP_HAUS_FUTURE=on\nSLOP_HAUS_LABELS=a\n")
        environ: dict[str, str] = {}

        names = run_env.apply(self.path, environ)

        self.assertEqual(["SLOP_HAUS_LABELS"], names)
        self.assertEqual({"SLOP_HAUS_LABELS": "a"}, environ)

    def test_expansions_stay_literal(self) -> None:
        self.path.write_text("A=$HOME/bin\nB=`id`\n")
        environ: dict[str, str] = {}

        run_env.apply(self.path, environ)

        self.assertEqual({"A": "$HOME/bin", "B": "`id`"}, environ)

    def test_git_never_prompts_and_an_operators_choice_stands(self) -> None:
        environ = {"GIT_SSH_COMMAND": "ssh -i k"}

        run_env.apply_git_defaults(environ)

        self.assertEqual({"GIT_SSH_COMMAND": "ssh -i k", "GIT_TERMINAL_PROMPT": "0"}, environ)
        fresh: dict[str, str] = {}
        run_env.apply_git_defaults(fresh)
        self.assertIn("BatchMode=yes", fresh["GIT_SSH_COMMAND"])
