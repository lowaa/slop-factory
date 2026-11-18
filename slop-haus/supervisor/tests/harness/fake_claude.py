"""A stand-in `claude` that speaks stream-json.

The mode comes from FAKE_MODE; what it was called with is logged to FAKE_DIR for the test to read.
"""

from __future__ import annotations

FAKE_CLAUDE = r'''
import json, os, signal, subprocess, sys, time

log_dir = os.environ["FAKE_DIR"]
with open(os.path.join(log_dir, "invocations.jsonl"), "a") as f:
    f.write(json.dumps({"argv": sys.argv[1:], "cwd": os.getcwd(),
                        "env": {k: v for k, v in os.environ.items() if k.startswith(("SLOP_HAUS", "CLAUDE_", "ANTHROPIC_", "KEEP"))}}) + "\n")
mode = os.environ.get("FAKE_MODE", "")
out = lambda record: print(json.dumps(record), flush=True)

if mode == "session-in-use" and "--resume" in sys.argv:
    sys.stderr.write("Error: Session ID x is already in use.\n")
    sys.exit(1)
if mode == "no-conversation" and "--resume" in sys.argv:
    out({"type": "result", "subtype": "error_during_execution", "is_error": True, "num_turns": 0, "modelUsage": {},
         "errors": [os.environ["FAKE_ERROR"]]})
    if not os.environ.get("FAKE_QUIET_STDERR"):
        sys.stderr.write(os.environ["FAKE_ERROR"] + "\n")
    sys.exit(1)

line = sys.stdin.readline()
text = json.loads(line)["message"]["content"][0]["text"]
with open(os.path.join(log_dir, "received.jsonl"), "a") as f:
    f.write(json.dumps({"text": text}) + "\n")

if mode == "stderr-only":
    sys.stderr.write(os.environ["FAKE_ERROR"] + "\n")
    sys.exit(1)
if mode == "late-stderr":
    # stdout closes well before the last of stderr is written
    os.close(1)
    time.sleep(0.5)
    sys.stderr.write(os.environ["FAKE_ERROR"] + "\n")
    sys.exit(1)
if mode == "exit-leaving-child":
    # a backgrounded command still holds stdout after the harness is gone
    subprocess.Popen(["sleep", "60"], start_new_session=True)
    sys.exit(3)
if mode == "crash":
    sys.stderr.write("boom: https://user:secret@example.com/x\n")
    sys.exit(3)

out({"type": "system", "subtype": "init", "apiKeySource": os.environ.get("FAKE_API_KEY_SOURCE", "none")})
if mode == "hang":
    # Does its work, then never answers; a command it backgrounded outlives it unless the tree is killed.
    child = subprocess.Popen(["sleep", "60"], start_new_session=True)
    with open(os.path.join(log_dir, "child.pid"), "w") as f:
        f.write(str(child.pid))
    open(os.path.join(log_dir, "hanging"), "w").close()
    signal.pause()
if mode in ("limit-event", "limit-event-stale"):
    reset = int(os.environ["FAKE_RESET"])
    out({"type": "rate_limit_event", "rate_limit_info": {"status": "rejected", "resetsAt": reset, "rateLimitType": "five_hour"}})

if mode == "finish-with-child":
    child = subprocess.Popen(["sleep", "60"], start_new_session=True)
    with open(os.path.join(log_dir, "child.pid"), "w") as f:
        f.write(str(child.pid))

if mode == "max-turns":
    out({"type": "result", "subtype": "error_max_turns", "is_error": os.environ.get("FAKE_IS_ERROR") == "1", "num_turns": 3,
         "modelUsage": {"claude-test-1": {"inputTokens": 5, "outputTokens": 1}}})
    sys.stdin.read()
    sys.exit(0)

error = os.environ.get("FAKE_ERROR")
if error:
    out({"type": "result", "subtype": "success", "is_error": True, "result": error, "num_turns": 1,
         "modelUsage": {"claude-test-1": {"inputTokens": 5, "outputTokens": 1}}})
else:
    out({"type": "assistant", "message": {"id": "m1", "model": "claude-test-1", "content": [{"type": "text", "text": "echo: " + text}]}})
    out({"type": "result", "subtype": "success", "is_error": False, "result": "echo: " + text, "num_turns": 2,
         "modelUsage": {
             "claude-test-1": {"inputTokens": 100, "outputTokens": 7, "cacheReadInputTokens": 10, "cacheCreationInputTokens": 3, "costUSD": 0.01},
             "claude-test-2": {"inputTokens": 20, "outputTokens": 2, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 1}}})
# a real claude lingers until stdin closes
sys.stdin.read()
'''
