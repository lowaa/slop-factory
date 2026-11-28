# Codex app-server recordings

What `codex app-server` (codex-cli 0.172.0, the real model, a ChatGPT
login) wrote to stdout, one scenario per file, for `codec_test.go` and the
strict decode in `protocol/protocol_test.go`.

| File | Scenario |
| --- | --- |
| `start.jsonl` | `initialize`, then `thread/start` with `approvalPolicy: never`, `sandbox: danger-full-access` and `developerInstructions` |
| `turn_completed.jsonl` | an idle `turn/start`, answered with text, through `turn/completed` |
| `turn_steered.jsonl` | a turn running `timeout 15 tail -f /dev/null`, with a `turn/steer` sent at the command's `item/started` |
| `steer_refused.jsonl` | `turn/steer` after its turn ended |
| `turn_interrupted.jsonl` | a turn running `sleep 97`, `turn/interrupt` at the command's `item/started`, then `thread/backgroundTerminals/clean` |
| `interrupt_refused.jsonl` | `turn/interrupt` after its turn ended |
| `resume.jsonl` | a fresh app-server's `initialize` and `thread/resume` of the thread above |
| `resume_missing.jsonl` | `thread/resume` of a thread with no rollout |
| `turn_guarded.jsonl` | a guarded turn, played after `start.jsonl`: `thread/start` with `approvalPolicy: untrusted`, `approvalsReviewer: user`, `sandbox: danger-full-access`; the scripted model (not the real one) runs `ls`, answered `accept`, then `gh pr merge 12 --squash`, answered `decline` with the reason sent as `turn/steer`, then replies `DONE`. Its `thread/started` and `warning` (no model metadata for `scripted-model-1`) are dropped too |

`rollout.jsonl` is not stdout: it is the rollout file Codex wrote for an
app-server thread (`thread/start`, then one `turn/start` asking for `cat
notes.txt`, `cat missing.txt` and the reply `DONE`), for
`transcript_test.go`. To record it again, drive that thread, copy the file
at `thread/start`'s `thread.path` once `turn/completed` arrives, and keep
only `session_meta` (its ids, `cwd`, `originator`, `cli_version`,
`source`), the `event_msg` records `task_started`, `task_complete`,
`item_completed` and `token_count` (its `info` only), and the assistant
`message` and `custom_tool_call(_output)` response_items; replace the
workspace path with `/ws`, and delete the thread (`thread/delete`).

## The id convention

A response's `id` is recorded as `"<method>"`, the method of the request it
answers. The test's player swaps in the id of the codec's own last request
of that method, so the fixtures don't fix the codec's numbering.

## Recording again

On a Codex version bump, after `go generate ./internal/harness/codex/protocol`:

1. Drive a fresh `codex app-server` (stdio) through the scenarios above with
   a small JSON-RPC client, sending `initialize` with
   `capabilities: {experimentalApi: true}`, as the harness does. Run it in a
   workspace under `~/workspace/.slop-bot-live-*`, and log every stdout line
   and every request sent.
2. Cut the log into the files above. Drop notifications the codec ignores
   that are large or personal: `turn/moderationMetadata`,
   `remoteControl/status/changed` (host name, installation id),
   `thread/settings/updated` (echoes the instructions), `account/updated`,
   `thread/readState/changed`, `deprecationNotice`, `thread/goal/cleared`.
3. Scrub:
   - every UUID to `00000000-0000-7000-8000-<n>`, numbered by first
     appearance, with each session's thread id mapped to `...000000000001`
     so the files share one thread
   - the workspace path to `/ws`, `$CODEX_HOME` to `/codex-home`
   - `userAgent` to `slop-bot/<version> (Linux; x86_64) (slop-bot; 0)`
   - response ids to `"<method>"`
4. Check no user name, host name or e-mail is left, then run the codec and
   protocol tests, and the live test (`SLOP_BOT_LIVE_CODEX=1`).
5. Delete the recorded threads through Codex (`thread/delete`) so they don't
   stay in the user's `codex resume` list.

`turn_guarded.jsonl` is recorded against the scripted model (a temp
`CODEX_HOME`, as tier 2 runs it), with a client that answers each approval
the way the codec does; the steered text is scrubbed to the guard's
opening words. The other approval requests and the failed turns in
`codec_test.go` aren't recorded: an unguarded Codex sends no approvals, and
a failure needs a failing model. They are written from the schema.
