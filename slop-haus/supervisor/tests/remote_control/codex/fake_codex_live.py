"""A stand-in `codex app-server` that stays up across turns, after the 0.165.0 protocol schema.

Every line it receives is logged to FAKE_DIR/received.jsonl, the invocation to invocations.jsonl. A turn echoes
its message as an agentMessage and reports thread-cumulative token usage (100 in, 20 cached, 7 out per turn;
kept in FAKE_DIR/usage.json, so a resumed thread goes on from it). FAKE_MODE: "hold" leaves a turn running until a
turn/steer (which ends it with both messages) or a turn/interrupt (which ends it interrupted) arrives,
"late-interrupt" answers turn/interrupt with the no-active-turn error, "resume-missing" answers thread/resume with the
no-rollout error, "fail" ends the turn failed with FAKE_ERROR, "turn-error" refuses turn/start, "ask" sends a command approval request first.
"""

from __future__ import annotations

FAKE_CODEX_LIVE = r'''
import json, os, sys

log_dir = os.environ["FAKE_DIR"]
mode = os.environ.get("FAKE_MODE", "")
THREAD = "0192f5a0-1111-7222-8333-444455556666"


def log(name, record):
    with open(os.path.join(log_dir, name), "a") as f:
        f.write(json.dumps(record) + "\n")


def out(record):
    print(json.dumps(record), flush=True)


def usage_total():
    path = os.path.join(log_dir, "usage.json")
    try:
        with open(path) as f:
            total = json.load(f)
    except OSError:
        total = {"inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 0}
    last = {"inputTokens": 100, "cachedInputTokens": 20, "outputTokens": 7, "reasoningOutputTokens": 2, "totalTokens": 107}
    for k, v in last.items():
        total[k] += v
    with open(path, "w") as f:
        json.dump(total, f)
    return total, last


log("invocations.jsonl", {"argv": sys.argv[1:], "cwd": os.getcwd(),
                          "env": {k: v for k, v in os.environ.items() if k.startswith(("SLOP_HAUS", "CODEX", "OPENAI", "KEEP"))}})
thread_id = THREAD
turns = 0
held = []


def finish(turn_id, status, error=None):
    total, last = usage_total()
    out({"method": "thread/tokenUsage/updated", "params": {"threadId": thread_id, "turnId": turn_id, "tokenUsage": {"total": total, "last": last}}})
    out({"method": "turn/completed", "params": {"threadId": thread_id, "turn": {"id": turn_id, "status": status, "items": [], "error": error}}})


def say(turn_id, text):
    out({"method": "item/completed", "params": {"threadId": thread_id, "turnId": turn_id, "item": {"type": "agentMessage", "id": "m", "text": "echo: " + text}}})


for line in sys.stdin:
    msg = json.loads(line)
    log("received.jsonl", msg)
    method, rid = msg.get("method"), msg.get("id")
    params = msg.get("params") or {}
    if method == "initialize":
        out({"id": rid, "result": {"userAgent": "fake"}})
    elif method in ("thread/start", "thread/resume"):
        if method == "thread/resume" and mode == "resume-missing":
            out({"id": rid, "error": {"code": -32600, "message": "no rollout found for thread id " + params["threadId"]}})
            continue
        thread_id = params.get("threadId", THREAD)
        out({"id": rid, "result": {"thread": {"id": thread_id}, "model": "gpt-test-1", "modelProvider": "openai"}})
    elif method == "turn/start":
        turns += 1
        turn_id = "turn-%d" % turns
        text = params["input"][0]["text"]
        if mode == "turn-error":
            out({"id": rid, "error": {"code": -32600, "message": os.environ["FAKE_ERROR"]}})
            continue
        out({"id": rid, "result": {"turn": {"id": turn_id, "status": "inProgress", "items": []}}})
        out({"method": "turn/started", "params": {"threadId": thread_id, "turn": {"id": turn_id, "status": "inProgress", "items": []}}})
        if mode == "ask":
            out({"id": 100, "method": "item/commandExecution/requestApproval", "params": {"threadId": thread_id}})
            log("answers.jsonl", json.loads(sys.stdin.readline()))
        if mode in ("hold", "late-interrupt"):
            held = [turn_id, text]
        elif mode == "fail":
            out({"method": "error", "params": {"threadId": thread_id, "turnId": turn_id, "willRetry": False, "error": {"message": os.environ["FAKE_ERROR"]}}})
            finish(turn_id, "failed", {"message": os.environ["FAKE_ERROR"]})
        else:
            say(turn_id, text)
            finish(turn_id, "completed")
    elif method == "thread/compact/start":
        turns += 1
        turn_id = "turn-%d" % turns
        out({"id": rid, "result": {}})
        out({"method": "turn/started", "params": {"threadId": thread_id, "turn": {"id": turn_id, "status": "inProgress", "items": []}}})
        out({"method": "item/completed", "params": {"threadId": thread_id, "turnId": turn_id, "item": {"type": "contextCompaction", "id": "c"}}})
        finish(turn_id, "completed")
    elif method == "turn/steer":
        if not held or params["expectedTurnId"] != held[0]:
            out({"id": rid, "error": {"code": -32600, "message": "no active turn to steer"}})
            continue
        out({"id": rid, "result": {"turnId": held[0]}})
        say(held[0], held[1] + "+" + params["input"][0]["text"])
        finish(held[0], "completed")
        held = []
    elif method == "turn/interrupt":
        if mode == "late-interrupt" or not held:
            out({"id": rid, "error": {"code": -32600, "message": "no active turn to interrupt"}})
            continue
        out({"id": rid, "result": {}})
        finish(held[0], "interrupted")
        held = []
'''
