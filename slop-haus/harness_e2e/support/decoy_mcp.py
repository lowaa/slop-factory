"""A stand-in for an MCP server the owner has that slop-haus's Remote Control
must keep out of a session (a project's .mcp.json entry, a connector): the
smallest stdio server Claude Code will connect to and take a tool from."""

from __future__ import annotations

import json
import sys

TOOL = "decoy_ping"


def handle(message: dict) -> dict | None:
    method = message.get("method")
    if "id" not in message:
        return None
    if method == "initialize":
        result = {
            "protocolVersion": message["params"]["protocolVersion"],
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "decoy", "version": "0"},
        }
    elif method == "tools/list":
        result = {"tools": [{"name": TOOL, "description": "does nothing", "inputSchema": {"type": "object", "properties": {}}}]}
    else:
        return {"jsonrpc": "2.0", "id": message["id"], "error": {"code": -32601, "message": "not here"}}
    return {"jsonrpc": "2.0", "id": message["id"], "result": result}


def main() -> None:
    for line in sys.stdin:
        if not line.strip():
            continue
        reply = handle(json.loads(line))
        if reply is not None:
            sys.stdout.write(json.dumps(reply) + "\n")
            sys.stdout.flush()


if __name__ == "__main__":
    main()
