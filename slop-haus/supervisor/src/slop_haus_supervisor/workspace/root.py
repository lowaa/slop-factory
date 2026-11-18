from __future__ import annotations

import tomllib
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import cast

ROOT_ENV = "SLOP_HAUS_HOME"
DEFAULT_ROOT = "~/slop-haus/{tenant}"
USER_CONFIG = Path(".config") / "slop-haus" / "supervisor.toml"
DEFAULT_TENANT_FILE = Path(".config") / "slop-haus" / "default_tenant"
HOST_ROOTS = "slop-haus"
CONFIG_NAME = "supervisor.toml"


class RootError(Exception):
    pass


@dataclass(frozen=True)
class RootSources:
    """Everything the lookup reads, so a test passes a temporary home and environment."""

    home: Path
    env: Mapping[str, str]

    @property
    def user_config(self) -> Path:
        return self.home / USER_CONFIG

    @property
    def default_tenant_file(self) -> Path:
        return self.home / DEFAULT_TENANT_FILE


def _expand(template: str, tenant: str, home: Path) -> Path:
    # Expanded by hand against `home` rather than os.path.expanduser, which reads $HOME
    # and so would make the lookup depend on state the caller did not hand in.
    text = template.replace("{tenant}", tenant)
    if text == "~" or text.startswith("~/"):
        return home / text[2:]
    return Path(text)


def _user_config_root(sources: RootSources, tenant: str) -> str | None:
    path = sources.user_config
    if not path.is_file():
        return None
    try:
        data = tomllib.loads(path.read_text())
    except tomllib.TOMLDecodeError as err:
        raise RootError(f"{path} is not valid TOML: {err}") from err
    tenants = data.get("tenants")
    if isinstance(tenants, dict):
        entry = cast(dict[str, object], tenants).get(tenant)
        if isinstance(entry, dict):
            tenant_root = cast(dict[str, object], entry).get("root")
            if isinstance(tenant_root, str):
                return tenant_root
    root = data.get("root")
    return root if isinstance(root, str) else None


def resolve_root(tenant: str, sources: RootSources, flag: str | None = None) -> Path:
    """The tenant's root: --root, then SLOP_HAUS_HOME, then the user config, then the default.

    `{tenant}` is substituted in every source, so one setting can serve several tenants.
    """
    if flag:
        return _expand(flag, tenant, sources.home)
    env_root = sources.env.get(ROOT_ENV)
    if env_root:
        return _expand(env_root, tenant, sources.home)
    configured = _user_config_root(sources, tenant)
    if configured:
        return _expand(configured, tenant, sources.home)
    return _expand(DEFAULT_ROOT, tenant, sources.home)


def host_roots(sources: RootSources) -> dict[str, Path]:
    """The roots set up under ~/slop-haus/, by directory name. Roots kept elsewhere are reached by --root or SLOP_HAUS_HOME."""
    base = sources.home / HOST_ROOTS
    if not base.is_dir():
        return {}
    return {d.name: d for d in sorted(base.iterdir()) if (d / CONFIG_NAME).is_file()}


def enclosing_root(cwd: Path) -> Path | None:
    """The nearest directory at or above `cwd` holding supervisor.toml; a slot's directory is inside its root."""
    for candidate in (cwd, *cwd.parents):
        if (candidate / CONFIG_NAME).is_file():
            return candidate
    return None


def default_tenant(sources: RootSources) -> str | None:
    path = sources.default_tenant_file
    if not path.is_file():
        return None
    return path.read_text().strip() or None


def set_default_tenant(sources: RootSources, tenant: str) -> None:
    path = sources.default_tenant_file
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"{tenant}\n")


def _pick_help(roots: dict[str, Path]) -> str:
    listing = "\n".join(f"  {name}  ({path})" for name, path in roots.items())
    return f"{listing}\npick one with -t/--tenant NAME for this command, or `slop-haus use NAME` to set the default"


def find_root(sources: RootSources, cwd: Path, flag_root: str | None = None, tenant: str = "") -> Path:
    """The root a command acts on.

    In order: --root, -t/--tenant, SLOP_HAUS_HOME, the root the current directory is inside, the
    only root on the host, the default set by `use`. Several roots with none of these is a refusal
    that lists them, never a guess: a command on the wrong tenant's slots is hard to undo.
    """
    if flag_root:
        return resolve_root(tenant, sources, flag_root)
    if tenant:
        # Asked for by name: a fixed SLOP_HAUS_HOME is some other tenant's, only a template can serve this one.
        env_root = sources.env.get(ROOT_ENV, "")
        env = sources.env if "{tenant}" in env_root else {k: v for k, v in sources.env.items() if k != ROOT_ENV}
        return resolve_root(tenant, RootSources(sources.home, env))
    env_root = sources.env.get(ROOT_ENV)
    # A template needs a tenant to be a root; without one it is no hint.
    if env_root and "{tenant}" not in env_root:
        return _expand(env_root, "", sources.home)
    inside = enclosing_root(cwd)
    if inside is not None:
        return inside
    roots = host_roots(sources)
    if len(roots) == 1:
        return next(iter(roots.values()))
    chosen = default_tenant(sources)
    if chosen:
        return resolve_root(chosen, sources)
    if not roots:
        raise RootError("no supervisor is set up on this host: run the setup script first (or say where with --tenant NAME or --root DIR)")
    raise RootError(f"several tenants are set up on this host:\n{_pick_help(roots)}")
