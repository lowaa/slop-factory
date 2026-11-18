from __future__ import annotations

import json
import os
import re
import shlex
import tempfile
from pathlib import Path

# Shared with workspace.ToolCallTimeout and pi-extension.ts; contracts/timeouts.json holds all three to one value.
TOOL_CALL_TIMEOUT_MS = 120_000
AGENT_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$")


def write_mcp_config(path: Path, server: str, token_script: Path) -> None:
    """An MCP config naming slop-haus alone, written from what the supervisor knows of its server.

    Never a file from the checkout, which a commit under discussion could have changed to run any
    command. The token comes from the slot's token.sh, which refreshes it under its own lock.
    """
    helper = f"{shlex.quote(str(token_script))} headers -server={shlex.quote(server)}"
    config = {"mcpServers": {"slop-haus": {
        "type": "http", "url": server.rstrip("/") + "/mcp", "headersHelper": helper, "timeout": TOOL_CALL_TIMEOUT_MS,
    }}}
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".tmp-")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(config, f)
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise
