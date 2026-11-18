"""Test harness for token.bats: runs slop_haus_token.py with its HTTP transport and sleep replaced
by the bats suite's file-driven stubs, so one suite exercises both implementations.

The bats stub for python3 swaps this in for `python3 -I .slop-haus/slop_haus_token.py`; the
module itself is loaded unmodified. Mirrors stub_curl and stub_sleep in token.bats, which
document the STATE_DIR files. Not shipped to workspaces.
"""

from __future__ import annotations

import importlib.util
import os
import shutil
import sys
import time
import urllib.parse

module_path = sys.argv[1]
spec = importlib.util.spec_from_file_location("slop_haus_token", module_path)
assert spec is not None and spec.loader is not None
tok = importlib.util.module_from_spec(spec)
sys.modules["slop_haus_token"] = tok
spec.loader.exec_module(tok)

STATE = os.environ["STATE_DIR"]


def read(name: str) -> str:
    with open(os.path.join(STATE, name), encoding="utf-8") as handle:
        return handle.read().rstrip("\n")


def append(path: str, line: str) -> None:
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(line + "\n")


def transport(url: str, fields, timeout: float):  # type: ignore[no-untyped-def]
    append(os.environ["CURL_LOG"], f"-sS --max-time {timeout:g} -X POST {url}")
    append(os.environ["CURL_BODY_LOG"], "&".join(f"{k}={urllib.parse.quote(v, safe='')}" for k, v in fields))
    grant = dict(fields).get("grant_type")
    if url.endswith("/oauth/device_authorization"):
        return tok.Response(int(read("device_auth.status")), read("device_auth.body"))
    if not url.endswith("/oauth/token"):
        return tok.Response(404, '{"error":"not_found"}')
    if grant != "refresh_token":
        return poll()
    append(os.path.join(STATE, "refresh.calls"), "refresh")
    if os.path.exists(os.path.join(STATE, "refresh.delay")):
        time.sleep(float(read("refresh.delay")))
    with open(os.path.join(STATE, "refresh.calls"), encoding="utf-8") as handle:
        n = len(handle.readlines())
    if os.path.exists(os.path.join(STATE, f"refresh.{n}.status")):
        name = f"refresh.{n}"
        if os.path.exists(os.path.join(STATE, f"{name}.credentials")):
            shutil.copyfile(os.path.join(STATE, f"{name}.credentials"), os.environ["CREDENTIALS"])
        if read(f"{name}.status") == "fail":
            return tok.Response(0)
    else:
        name = "refresh"
    return tok.Response(int(read(f"{name}.status")), read(f"{name}.body"))


def poll():  # type: ignore[no-untyped-def]
    count_path = os.path.join(STATE, "poll.count")
    n = 1
    if os.path.exists(count_path):
        n = int(read("poll.count")) + 1
    with open(count_path, "w", encoding="utf-8") as handle:
        handle.write(f"{n}\n")
    name = f"poll.{n}" if os.path.exists(os.path.join(STATE, f"poll.{n}.body")) else "poll"
    return tok.Response(int(read(f"{name}.status")), read(f"{name}.body"))


def sleep(seconds: float) -> None:
    append(os.environ["SLEEP_LOG"], str(seconds))


root = os.path.dirname(os.path.dirname(os.path.abspath(module_path)))
sys.exit(tok.main(sys.argv[2:], root, os.environ, transport, sleep))
