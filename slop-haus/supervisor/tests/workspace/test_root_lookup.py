from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.root import (
    ROOT_ENV,
    RootError,
    RootSources,
    default_tenant,
    find_root,
    set_default_tenant,
)
from support import TempDirTestCase


class FindRootTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "elsewhere"
        self.cwd.mkdir()

    def root(self, tenant: str) -> Path:
        path = self.home / "slop-haus" / tenant
        path.mkdir(parents=True)
        (path / "supervisor.toml").write_text(SupervisorConfig("https://s", f"id-{tenant}", tenant).dumps())
        return path

    def find(self, env: dict[str, str] | None = None, cwd: Path | None = None, root: str | None = None, tenant: str = "") -> Path:
        return find_root(RootSources(self.home, env or {}), cwd or self.cwd, root, tenant)

    def test_the_only_root_on_the_host_is_the_one(self) -> None:
        only = self.root("acme")

        self.assertEqual(only, self.find())

    def test_several_roots_and_no_hint_refuse_and_list_them(self) -> None:
        self.root("acme")
        self.root("globex")

        with self.assertRaises(RootError) as caught:
            self.find()

        message = str(caught.exception)
        self.assertIn("acme", message)
        self.assertIn("globex", message)
        self.assertIn("slop-haus use", message)
        self.assertIn("--tenant", message)

    def test_no_roots_at_all_says_to_run_setup(self) -> None:
        with self.assertRaisesRegex(RootError, "setup script"):
            self.find()

    def test_inside_a_slot_directory_is_its_root(self) -> None:
        self.root("acme")
        other = self.root("globex")
        slot = other / "slots" / "host-1" / "src" / "deep"
        slot.mkdir(parents=True)

        self.assertEqual(other, self.find(cwd=slot))

    def test_use_sets_the_default_among_several_roots(self) -> None:
        self.root("acme")
        chosen = self.root("globex")
        set_default_tenant(RootSources(self.home, {}), "globex")

        self.assertEqual("globex", default_tenant(RootSources(self.home, {})))
        self.assertEqual(chosen, self.find())

    def test_a_flag_beats_the_default_and_the_environment(self) -> None:
        acme = self.root("acme")
        self.root("globex")
        set_default_tenant(RootSources(self.home, {}), "globex")
        env = {ROOT_ENV: str(self.tmp / "from-env")}

        self.assertEqual(acme, self.find(env, tenant="acme"))
        self.assertEqual(Path("/from/flag"), self.find(env, root="/from/flag"))

    def test_the_environment_beats_the_current_directory(self) -> None:
        inside = self.root("acme") / "slots"
        inside.mkdir()
        elsewhere = self.tmp / "from-env"

        self.assertEqual(elsewhere, self.find({ROOT_ENV: str(elsewhere)}, cwd=inside))

    def test_a_templated_environment_root_is_no_hint_without_a_tenant(self) -> None:
        only = self.root("acme")

        self.assertEqual(only, self.find({ROOT_ENV: str(self.tmp / "{tenant}")}))

    def test_a_folder_without_supervisor_toml_is_not_a_root(self) -> None:
        (self.home / "slop-haus" / "stray").mkdir(parents=True)
        only = self.root("acme")

        self.assertEqual(only, self.find())
