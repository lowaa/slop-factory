"""A ticket's variant: which model, and how hard it thinks."""

from __future__ import annotations

import re
from dataclasses import dataclass

# The same patterns as harness-config.py and the server's harnessconfig: each starts with an
# alphanumeric, so no value can be read as another flag.
MODEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$")
PROVIDER = re.compile(r"^[a-z0-9][a-z0-9._-]{0,31}\Z")
CLAUDE_EFFORT = ("low", "medium", "high", "xhigh", "max", "auto")
CODEX_EFFORT = ("low", "medium", "high", "xhigh", "max", "ultra")
PI_THINKING = ("off", "minimal", "low", "medium", "high", "xhigh", "max")


class InvalidVariant(Exception):
    pass


@dataclass(frozen=True)
class Variant:
    model: str = ""
    # Claude's effort level; pi spells its own `thinking`, as the server's harness config does.
    effort: str = ""
    thinking: str = ""
    provider: str = ""
    subagent_model: str = ""


KEYS = {"model", "effort", "thinking", "provider", "subagent_model"}


def parse_variant(text: str) -> Variant:
    """`model=claude-opus-4-7,effort=high` (claude) or `provider=anthropic,model=x,thinking=high` (pi); empty means the harness's own defaults.

    Everything ends up on a command line or in an environment, so each value is checked here.
    """
    fields: dict[str, str] = {}
    for part in filter(None, (p.strip() for p in text.split(","))):
        key, sep, value = part.partition("=")
        if not sep or key not in KEYS:
            raise InvalidVariant(f"unknown variant setting {part!r}")
        if key in fields:
            raise InvalidVariant(f"variant setting {key!r} given twice")
        fields[key] = value
    for key in ("model", "subagent_model"):
        if key in fields and not MODEL.match(fields[key]):
            raise InvalidVariant(f"{key} {fields[key]!r} is not something a harness takes")
    if "provider" in fields and not PROVIDER.match(fields["provider"]):
        raise InvalidVariant(f"provider {fields['provider']!r} is not something a harness takes")
    return Variant(**fields)


def check_choice(name: str, value: str, allowed: tuple[str, ...]) -> None:
    if value and value not in allowed:
        raise InvalidVariant(f"{name} {value!r} is not one of {', '.join(allowed)}")
