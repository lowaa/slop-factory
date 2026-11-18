from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.workspace.root import ROOT_ENV, RootError, RootSources, resolve_root
from support import TempDirTestCase


class ResolveRootTest(TempDirTestCase):
    def sources(self, env: dict[str, str] | None = None, user_config: str | None = None) -> RootSources:
        home = self.tmp / "home"
        home.mkdir(exist_ok=True)
        if user_config is not None:
            path = home / ".config" / "slop-haus" / "supervisor.toml"
            path.parent.mkdir(parents=True)
            path.write_text(user_config)
        return RootSources(home, env or {})

    def test_default_is_under_the_home_by_tenant(self) -> None:
        s = self.sources()
        self.assertEqual(s.home / "slop-haus" / "acme", resolve_root("acme", s))

    def test_flag_beats_everything(self) -> None:
        s = self.sources({ROOT_ENV: "/from/env"}, 'root = "/from/config/{tenant}"')
        self.assertEqual(Path("/from/flag"), resolve_root("acme", s, flag="/from/flag"))

    def test_env_beats_user_config(self) -> None:
        s = self.sources({ROOT_ENV: "/from/env/{tenant}"}, 'root = "/from/config/{tenant}"')
        self.assertEqual(Path("/from/env/acme"), resolve_root("acme", s))

    def test_user_config_template_and_tilde(self) -> None:
        s = self.sources(user_config='root = "~/work/{tenant}"')
        self.assertEqual(s.home / "work" / "acme", resolve_root("acme", s))

    def test_tenant_table_beats_the_template(self) -> None:
        s = self.sources(user_config='root = "/data/{tenant}"\n[tenants.acme]\nroot = "/special/acme"\n')
        self.assertEqual(Path("/special/acme"), resolve_root("acme", s))
        self.assertEqual(Path("/data/other"), resolve_root("other", s))

    def test_a_user_config_without_a_root_falls_through_to_the_default(self) -> None:
        s = self.sources(user_config='[tenants.other]\nroot = "/x"\n')
        self.assertEqual(s.home / "slop-haus" / "acme", resolve_root("acme", s))

    def test_a_broken_user_config_is_an_error_not_a_silent_default(self) -> None:
        s = self.sources(user_config="root = ")
        with self.assertRaises(RootError):
            resolve_root("acme", s)
