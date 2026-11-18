from __future__ import annotations

from collections.abc import Callable

from slop_haus_supervisor.slophaus.credentials import write_credentials
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.layout import Layout, SlotService


def reissue_slot_credentials(layout: Layout, service: SlotService, only: list[str] | None = None, say: Callable[[str], None] = print) -> int:
    """Give every configured slot a fresh credential, for slots whose own were revoked with the supervisor's grant.

    `only` limits it to those names. Returns how many could not be reissued. Each is named on `say`, so one slot's refusal does not hide the others.
    """
    config = cfg.load(layout.config_file)
    failed = 0
    if only is not None and (unknown := [n for n in only if n not in config.slots]):
        raise cfg.ConfigError(f"no slot {unknown[0]!r}")
    for name in config.slots if only is None else only:
        try:
            slot = service.find_slot(name)
            if slot is None:
                raise cfg.ConfigError("the server has no live slot of that name for this supervisor")
            write_credentials(layout.slot(name), config.server, service.issue_credential(slot))
        except (cfg.ConfigError, OSError) as err:
            failed += 1
            say(f"slot {name}: credential not reissued: {err}")
            continue
        say(f"slot {name}: credential reissued")
    return failed
