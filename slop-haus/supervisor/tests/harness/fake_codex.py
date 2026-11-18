"""A stand-in `codex app-server`, after the 0.165.0 protocol schema (see test_codex_schema.py).

Every line it receives is logged to FAKE_DIR/received.jsonl, the invocation to invocations.jsonl.
FAKE_MODE: "fail" ends the turn `failed` with FAKE_ERROR, "interrupted" ends it interrupted, "ask" sends a server request of
each kind FAKE_ASKS (comma separated) before the turn completes, "resume-missing" answers thread/resume with
the no-rollout error, "resume-other-error" with FAKE_ERROR, "bad-thread-id" starts a thread whose id is no UUID,
"exit-at-start" exits with FAKE_ERROR on stderr, "retry-error" sends a retried error and then completes. FAKE_USAGE (json: {"replay": breakdown, "total": breakdown,
"last": breakdown}) sends thread/tokenUsage/updated: `replay` before turn/started, the others during the turn.
"""

from __future__ import annotations

FAKE_CODEX = r'''
import json, os, sys

log_dir = os.environ["FAKE_DIR"]
mode = os.environ.get("FAKE_MODE", "")
THREAD = "0192f5a0-1111-7222-8333-444455556666"


def log(name, record):
    with open(os.path.join(log_dir, name), "a") as f:
        f.write(json.dumps(record) + "\n")


def out(record):
    print(json.dumps(record), flush=True)


log("invocations.jsonl", {"argv": sys.argv[1:], "cwd": os.getcwd(),
                          "env": {k: v for k, v in os.environ.items() if k.startswith(("SLOP_HAUS", "CODEX", "OPENAI", "KEEP"))}})
if mode == "exit-at-start":
    sys.stderr.write(os.environ["FAKE_ERROR"] + "\n")
    sys.exit(1)

thread_id = THREAD
for line in sys.stdin:
    msg = json.loads(line)
    log("received.jsonl", msg)
    method, rid = msg.get("method"), msg.get("id")
    if method == "initialize":
        out({"id": rid, "result": {"userAgent": "fake"}})
    elif method == "thread/start":
        thread_id = "not-a-uuid" if mode == "bad-thread-id" else THREAD
        out({"id": rid, "result": {"thread": {"id": thread_id}, "model": "gpt-test-1", "modelProvider": "scripted"}})
    elif method == "thread/resume":
        if mode == "resume-missing":
            out({"id": rid, "error": {"code": -32600, "message": "no rollout found for thread id " + msg["params"]["threadId"]}})
        elif mode == "resume-other-error":
            out({"id": rid, "error": {"code": -32600, "message": os.environ["FAKE_ERROR"]}})
        else:
            thread_id = msg["params"]["threadId"]
            out({"id": rid, "result": {"thread": {"id": thread_id}, "model": "gpt-test-1", "modelProvider": "scripted"}})
    elif method == "turn/start":
        usage = json.loads(os.environ.get("FAKE_USAGE", "{}"))

        def token_usage(total, last):
            out({"method": "thread/tokenUsage/updated", "params": {"threadId": thread_id, "turnId": "turn-1",
                                                                   "tokenUsage": {"total": total, "last": last}}})

        out({"id": rid, "result": {"turn": {"id": "turn-1", "status": "inProgress", "items": []}}})
        if "other_thread" in usage:
            out({"method": "thread/tokenUsage/updated", "params": {"threadId": "someone-else", "tokenUsage": {
                "total": usage["other_thread"], "last": usage["other_thread"]}}})
        if "replay" in usage:
            token_usage(usage["replay"], usage["replay"])
        out({"method": "turn/started", "params": {"threadId": thread_id, "turn": {"id": "turn-1", "status": "inProgress", "items": []}}})
        if "total" in usage:
            token_usage(usage["total"], usage["last"])
        for n, ask in enumerate(filter(None, os.environ.get("FAKE_ASKS", "").split(",")) if mode == "ask" else []):
            out({"id": 100 + n, "method": ask, "params": {"threadId": thread_id}})
            log("answers.jsonl", json.loads(sys.stdin.readline()))
        if mode == "retry-error":
            out({"method": "error", "params": {"threadId": thread_id, "turnId": "turn-1", "willRetry": True, "error": {"message": "401 Unauthorized (retrying)"}}})
        if mode in ("fail", "interrupted"):
            out({"method": "error", "params": {"threadId": thread_id, "turnId": "turn-1", "willRetry": False, "error": {"message": os.environ["FAKE_ERROR"]}}})
            out({"method": "turn/completed", "params": {"threadId": thread_id, "turn": {
                "id": "turn-1", "status": "failed" if mode == "fail" else "interrupted", "items": [],
                "error": {"message": os.environ["FAKE_ERROR"]} if mode == "fail" else None}}})
        else:
            out({"method": "turn/completed", "params": {"threadId": "someone-else", "turn": {"id": "t", "status": "failed", "items": []}}})
            out({"method": "turn/completed", "params": {"threadId": thread_id, "turn": {"id": "turn-1", "status": "completed", "items": []}}})
'''
