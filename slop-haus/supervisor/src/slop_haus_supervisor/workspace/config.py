from __future__ import annotations

import json
import os
import tempfile
import re
import tomllib
from typing import cast
from dataclasses import dataclass, field, replace
from pathlib import Path

NAME_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")


class ConfigError(Exception):
    pass


def check_name(kind: str, name: str) -> str:
    """Tenant and slot names become directory names, so they may not carry a path."""
    if not NAME_PATTERN.fullmatch(name):
        raise ConfigError(f"{kind} name {name!r} must be letters, digits, '.', '_' or '-', and start with a letter or digit")
    return name


@dataclass(frozen=True)
class SupervisorConfig:
    server: str
    tenant_id: str
    tenant: str
    harness: str = "claude"
    variant: str = ""
    slots: tuple[str, ...] = field(default_factory=lambda: ())
    # How this host clones: "ssh" or "https", decided once at the first `slot add`; "" until then.
    clone_protocol: str = ""

    def with_slot(self, name: str) -> SupervisorConfig:
        if name in self.slots:
            return self
        return replace(self, slots=(*self.slots, name))

    def without_slot(self, name: str) -> SupervisorConfig:
        return replace(self, slots=tuple(s for s in self.slots if s != name))

    def with_clone_protocol(self, protocol: str) -> SupervisorConfig:
        return replace(self, clone_protocol=protocol)

    def dumps(self) -> str:
        # JSON string syntax is valid TOML basic-string syntax, which is what keeps this writer
        # to a few lines and correct for any value.
        slots = ", ".join(json.dumps(s) for s in self.slots)
        return (
            f"server = {json.dumps(self.server)}\n"
            f"tenant_id = {json.dumps(self.tenant_id)}\n"
            f"tenant = {json.dumps(self.tenant)}\n"
            f"default_harness = {json.dumps(self.harness)}\n"
            f"default_variant = {json.dumps(self.variant)}\n"
            f"slots = [{slots}]\n"
            + (f"clone_protocol = {json.dumps(self.clone_protocol)}\n" if self.clone_protocol else "")
        )


def _text(data: dict[str, object], key: str, default: str | None = None) -> str:
    value = data.get(key, default)
    if not isinstance(value, str):
        raise ConfigError(f"supervisor.toml: {key} must be a string")
    return value


CLONE_PROTOCOLS = ("ssh", "https")


def _protocol(data: dict[str, object]) -> str:
    value = _text(data, "clone_protocol", "")
    if value and value not in CLONE_PROTOCOLS:
        raise ConfigError(f"supervisor.toml: clone_protocol must be one of {', '.join(CLONE_PROTOCOLS)}")
    return value


def loads(text: str) -> SupervisorConfig:
    try:
        data = tomllib.loads(text)
    except tomllib.TOMLDecodeError as err:
        raise ConfigError(f"supervisor.toml is not valid TOML: {err}") from err
    raw_slots = data.get("slots", [])
    if not isinstance(raw_slots, list) or not all(isinstance(s, str) for s in cast(list[object], raw_slots)):
        raise ConfigError("supervisor.toml: slots must be a list of names")
    slots = tuple(check_name("slot", s) for s in cast(list[str], raw_slots))
    return SupervisorConfig(
        server=_text(data, "server"),
        tenant_id=_text(data, "tenant_id"),
        tenant=check_name("tenant", _text(data, "tenant")),
        harness=_text(data, "default_harness", "claude"),
        variant=_text(data, "default_variant", ""),
        slots=slots,
        clone_protocol=_protocol(data),
    )


def load(path: Path) -> SupervisorConfig:
    try:
        return loads(path.read_text())
    except FileNotFoundError as err:
        raise ConfigError(f"{path} does not exist -- run the supervisor setup script first") from err


def write(path: Path, config: SupervisorConfig) -> None:
    """Replace supervisor.toml in one rename, so a crash never leaves half a file."""
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".supervisor.toml-")
    with os.fdopen(fd, "w") as out:
        out.write(config.dumps())
    os.replace(tmp, path)
    path.chmod(0o644)
