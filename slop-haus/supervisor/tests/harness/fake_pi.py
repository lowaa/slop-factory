"""A stand-in `pi --mode rpc`, after pi 0.99.1's RPC docs.

A message that starts with "tool" makes a failing tool call first, "run:" runs the rest in the working
directory, "error" ends the turn with FAKE_ERROR (or a default), and "/handled" is taken by an
"extension". FAKE_MODE: "error" fails every turn, "reject" refuses every prompt, "ui" asks a dialog,
"no-streaming" sends no text deltas, "close-stdin" closes its stdin and lives on, "exit-at-start"
exits with FAKE_ERROR on stderr. received.jsonl also holds a "-settled-" marker where each turn settled. A file FAKE_DIR/stats-fail-once makes the next stats request fail.

Session totals are kept in FAKE_DIR/stats.json, as pi keeps them in the session, so a second
process resuming the conversation reports totals that include the first's.
"""

from __future__ import annotations

FAKE_PI = r'''
import json, os, signal, sys

log_dir = os.environ["FAKE_DIR"]


def log(name, record):
    with open(os.path.join(log_dir, name), "a") as f:
        f.write(json.dumps(record) + "\n")


log("invocations.jsonl", {"argv": sys.argv[1:], "cwd": os.getcwd(),
                          "env": {k: v for k, v in os.environ.items() if k.startswith(("SLOP_HAUS", "PI_", "KEEP"))}})
mode = os.environ.get("FAKE_MODE", "")
if mode == "exit-at-start":
    sys.stderr.write(os.environ["FAKE_ERROR"] + "\n")
    sys.exit(1)
if mode == "close-stdin":
    os.close(0)
    log("closed", {})
    signal.pause()

stats_path = os.path.join(log_dir, "stats.json")


def load():
    try:
        with open(stats_path) as f:
            return json.load(f)
    except OSError:
        return {"assistantMessages": 0, "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "cost": 0.0}


def out(record):
    print(json.dumps(record), flush=True)


def assistant(text, s, error=None, stop="stop"):
    out({"type": "message_start", "message": {"role": "assistant", "content": []}})
    if text and mode != "no-streaming":
        for part in (text[:3], text[3:]):
            out({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": part}})
    message = {"role": "assistant", "provider": os.environ.get("FAKE_PROVIDER", "anthropic"), "model": "claude-test-1",
               "stopReason": "error" if error else stop, "content": [{"type": "text", "text": text}] if text else []}
    if error:
        message["errorMessage"] = error
    out({"type": "message_end", "message": message})
    s["assistantMessages"] += 1
    s["input"] += 100
    s["output"] += 7
    s["cacheRead"] += 10
    s["cost"] = round(s["cost"] + 0.001, 6)


for line in sys.stdin:
    command = json.loads(line)
    log("received.jsonl", command)
    kind = command["type"]
    if kind == "get_session_stats":
        fail_once = os.path.join(log_dir, "stats-fail-once")
        if os.path.exists(fail_once):
            os.remove(fail_once)
            out({"id": command["id"], "type": "response", "command": "get_session_stats", "success": False, "error": "session is busy"})
            continue
        s = load()
        out({"id": command["id"], "type": "response", "command": "get_session_stats", "success": True, "data": {
            "assistantMessages": s["assistantMessages"], "cost": s["cost"],
            "tokens": {"input": s["input"], "output": s["output"], "cacheRead": s["cacheRead"], "cacheWrite": s["cacheWrite"]},
            "contextUsage": {"tokens": 1000 + 10 * s["assistantMessages"], "contextWindow": 200000, "percent": 1}}})
        continue
    if kind == "compact":
        out({"id": command.get("id"), "type": "response", "command": "compact", "success": True,
             "data": {"summary": "s", "firstKeptEntryId": "e", "tokensBefore": 1000, "estimatedTokensAfter": 321}})
        continue
    if kind != "prompt":
        continue
    if mode == "reject":
        out({"id": command.get("id"), "type": "response", "command": "prompt", "success": False, "error": os.environ["FAKE_ERROR"]})
        continue
    text = command["message"]
    if mode == "handled" or text.startswith("/handled"):
        out({"id": command.get("id"), "type": "response", "command": "prompt", "success": True, "data": {"disposition": "handled"}})
        continue
    out({"id": command.get("id"), "type": "response", "command": "prompt", "success": True, "data": {"disposition": "started"}})
    if mode == "ui":
        out({"type": "extension_ui_request", "id": "ui-1", "method": "confirm", "title": "Run it?"})
        log("received.jsonl", json.loads(sys.stdin.readline()))
    if text.startswith("run:"):
        import subprocess
        subprocess.run(text[4:], shell=True, cwd=os.getcwd(), check=True, capture_output=True)
    s = load()
    out({"type": "agent_start"})
    if text.startswith("tool"):
        assistant("", s, stop="toolUse")
        out({"type": "tool_execution_start", "toolCallId": "t1", "toolName": "read", "args": {}})
        out({"type": "tool_execution_end", "toolCallId": "t1", "toolName": "read", "result": {}, "isError": True})
    error = os.environ.get("FAKE_ERROR", "529 overloaded") if mode == "error" or text.startswith("error") else None
    assistant("echo: " + text, s, error)
    with open(stats_path, "w") as f:
        json.dump(s, f)
    out({"type": "agent_end", "messages": [], "willRetry": False})
    log("received.jsonl", {"type": "-settled-"})  # when it settled, among the commands it was sent
    out({"type": "agent_settled"})
'''
