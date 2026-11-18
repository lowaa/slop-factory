from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.harness.variant import PI_THINKING, InvalidVariant, check_choice, parse_variant
from slop_haus_supervisor.remote_control.wire.errors import Refused


def pi_variant(provider: str, model: str, thinking: str) -> str:
    """The harness variant for pi's model flags. They end up on a command line, so they are
    checked here, where a bad one stops the host before it serves anything."""
    parts = [f"{key}={value}" for key, value in (("provider", provider), ("model", model), ("thinking", thinking)) if value]
    variant = ",".join(parts)
    try:
        check_choice("thinking", parse_variant(variant).thinking, PI_THINKING)
    except InvalidVariant as e:
        raise Refused(f"{e}; that is not something pi takes") from None
    return variant


def pi_extension(workspace: Path) -> Path:
    """The extension that gives pi slop-haus's tools, as setup installed it."""
    extension = workspace / ".slop-haus" / "pi" / "slop-haus.ts"
    if not extension.is_file() or extension.is_symlink():
        # A link would run something else.
        raise Refused(
            f"{extension} is missing or a link -- restart the Remote Control, which updates this workspace first (not with --no-update)"
        )
    return extension
