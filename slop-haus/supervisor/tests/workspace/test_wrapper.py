from __future__ import annotations

import os
import subprocess
from pathlib import Path

from slop_haus_supervisor.workspace import wrapper
from support import TempDirTestCase


class WrapperTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        # The script finds the root by comparing $HOME with the physical cwd, which macOS reports under /private/var.
        self.home = self.tmp.resolve() / "home"
        self.root = self.home / "slop-haus" / "acme"
        (self.root / "app").mkdir(parents=True)
        (self.root / "supervisor.toml").write_text("")
        bin_dir = self.tmp / "bin"
        bin_dir.mkdir()
        # A uv that only reports how it was called.
        uv = bin_dir / "uv"
        uv.write_text('#!/usr/bin/env bash\nprintf "%s\\n" "$@"\n')
        uv.chmod(0o755)
        self.path = f"{bin_dir}:{os.environ['PATH']}"

    def run_wrapper(self, cwd: Path, *args: str, env: dict[str, str] | None = None) -> list[str]:
        done = subprocess.run(
            [str(self.home / ".local" / "bin" / "slop-haus"), *args],
            cwd=cwd, env={"PATH": self.path, "HOME": str(self.home), **(env or {})}, capture_output=True, text=True, check=True,
        )  # fmt: skip
        return done.stdout.splitlines()

    def test_it_runs_the_supervisor_from_any_directory(self) -> None:
        wrapper.install(self.home, self.root, {})
        elsewhere = self.tmp / "anywhere"
        elsewhere.mkdir()

        out = self.run_wrapper(elsewhere, "slot", "add")

        self.assertEqual(
            ["run", "--frozen", "--no-dev", "--project", str(self.root / "app"), "slop-haus-supervisor", "slot", "add"], out
        )

    def test_inside_another_root_it_runs_that_roots_app(self) -> None:
        wrapper.install(self.home, self.root, {})
        other = self.home / "slop-haus" / "globex"
        (other / "app").mkdir(parents=True)
        (other / "supervisor.toml").write_text("")
        (other / "slots").mkdir()

        out = self.run_wrapper(other / "slots", "status")

        self.assertIn(str(other / "app"), out)

    def test_a_checkout_that_looks_like_a_root_does_not_choose_the_code_that_runs(self) -> None:
        wrapper.install(self.home, self.root, {})
        checkout = self.tmp / "checkout"
        (checkout / "app").mkdir(parents=True)
        (checkout / "supervisor.toml").write_text("")

        out = self.run_wrapper(checkout, "status")

        self.assertIn(str(self.root / "app"), out)
        self.assertNotIn(str(checkout / "app"), out)

    def test_it_tells_the_person_when_the_directory_is_not_on_the_path(self) -> None:
        notes = wrapper.install(self.home, self.root, {"PATH": "/usr/bin"})

        self.assertEqual(2, len(notes))
        self.assertIn(f'export PATH="{self.home / ".local" / "bin"}:$PATH"', notes[1])
        self.assertEqual(1, len(wrapper.install(self.home, self.root, {"PATH": str(self.home / ".local" / "bin")})))

    def test_a_command_of_someone_elses_is_not_overwritten(self) -> None:
        theirs = self.home / ".local" / "bin" / "slop-haus"
        theirs.parent.mkdir(parents=True)
        theirs.write_text("#!/bin/sh\necho mine\n")

        notes = wrapper.install(self.home, self.root, {})

        self.assertEqual("#!/bin/sh\necho mine\n", theirs.read_text())
        self.assertTrue((self.root / "bin" / "slop-haus").is_file())
        self.assertIn(str(self.root / "bin" / "slop-haus"), notes[0])
