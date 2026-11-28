# scriptedmodel

A model server that plays back a script, so the real harness CLIs (Claude Code, Codex, pi, agy) can run in
tests with no real model, no credential and no token spend. It is the Go port of
`slop-haus/harness_e2e/scripted_model` (Python), whose tests and design it keeps, with a Gemini adapter added
for agy. `cmd/scripted-model` serves it as a binary for tier 2's containers.

## Design

- A **scenario** is an ordered list of **turns**: text and/or tool calls, scripted usage numbers, and an
  optional **gate**. It is independent of any wire API, so one scenario runs on every harness.
- Each **wire adapter** turns a request into a `Request` (model, system text, offered tool names, the messages,
  all their text, the tool results alone) and a turn back into that API's response, streamed or not.
- Every request is **recorded**. A request the script did not expect (it ran past the end, asked for a tool the
  harness did not offer, no scenario loaded, an API or method this server does not speak, a body it cannot parse)
  is an **error**, returned to the harness in its API's error shape **and** recorded, so a test asserts the
  errors are empty.
- A request with **no tools** is taken to be the harness's own housekeeping (a title, a quota probe): it gets the
  text `ok` and does not use up the script (`Request.Auxiliary`, left out of `Recording.Conversation`).
- A gated turn waits until the test **releases** its gate: how a test acts between two turns without sleeping.
  A gate released before its request arrives (in the same scenario) stays open. `WaitHeld(ctx, gate)` returns once a request waits there, the
  synchronisation point for "the harness has asked for that turn". Loading or resetting the scenario releases
  every held request, so it cannot time out into the next scenario's errors. A gate nobody opens fails after
  `GateTimeout` (default 60s).

## In-process API (Go tests)

```go
server, err := scriptedmodel.New(&scriptedmodel.Options{ // nil for defaults
	Addr:        "",                // default 127.0.0.1:0
	Logger:      logger,            // *slog.Logger; default discards
	GateTimeout: 0,                 // default DefaultGateTimeout (60s)
	Models:      nil,               // what the model lists serve; default DefaultModels
})
defer server.Close()                // also releases held requests
server.URL()                        // http://127.0.0.1:<port>: point the harness here
server.Load(scenario)               // validate, replace, reset the recording
server.Append(turns...)             // extend the loaded scenario; the recording and turns played stay
server.Reset()
server.Release("gate")
server.WaitHeld(ctx, "gate")       // a request waits there; ErrGateRetired if the scenario is replaced
rec := server.Recording()           // a copy: rec.Requests, rec.Errors, rec.Conversation()
server.Remaining()
```

`ParseScenario(json)` reads a scenario file; unknown fields are refused, so a typo fails loudly.

## Out of process

Control endpoints, the Python server's paths and semantics, plus `held`:

| Method and path | |
| --- | --- |
| `PUT /_scripted/scenario` | load a scenario (resets the recording); 400 on a bad one |
| `DELETE /_scripted/scenario` | reset |
| `GET /_scripted/requests` | every request received, in order |
| `GET /_scripted/errors` | the unexpected-request failures |
| `GET /_scripted/remaining` | `{"remaining": n}` |
| `PUT /_scripted/gates/{name}` | release a gate |
| `GET /_scripted/gates/{name}/held` | new: blocks until a request waits at the gate; 409 if the scenario is replaced first |
| `GET /healthz` | `{"ok": true}` |

`Client` wraps them for a Go test driving a container (`NewClient(url, nil).Load(ctx, scenario)`, `Release`,
`HeldGate`, `Requests`, `Errors`, `Remaining`, `Reset`). The binary:

```
scripted-model [-listen 127.0.0.1:8000] [-scenario file.json] [-gate-timeout 60s] [-models a,b]
scripted-model ctl [-url URL] load FILE | reset | release GATE | held GATE | requests [-conversation] | errors | remaining
```

## Wire adapters

| API | Routes | Harness |
| --- | --- | --- |
| Anthropic Messages | `POST /v1/messages` (query ignored, e.g. `?beta=true`), `stream` true or false | Claude Code |
| OpenAI chat completions | `POST /v1/chat/completions`, ends with the usage-only chunk and `[DONE]` | pi |
| OpenAI Responses | `POST /v1/responses`: `response.created`, one `response.output_item.done` per item, `response.completed` with usage; MCP tools offered in a `namespace` tool are called with that namespace | Codex |
| Gemini | `POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent?alt=sse` (also under `/v1/models/`); `GET /v1beta/models`, `GET /v1/models` | agy |

Usage maps onto each API: Anthropic's four counters as is; OpenAI and Gemini count cached tokens inside the
prompt count (`prompt_tokens`/`input_tokens`/`promptTokenCount` = input + cache read, with the cached part in
`cached_tokens`/`cachedContentTokenCount`), and have no cache-write counter, so `cache_creation_input_tokens` is
dropped.

Gemini, from Google's API reference and what agy 1.5.18 sends:

- Request: `contents[]` of `{role: user|model, parts[]}`, a part being `text` (with `thought: true` for the
  model's thoughts, which are left out of `MessageText`), `functionCall {name, args}` or
  `functionResponse {name, response}`; `systemInstruction`; `tools[].functionDeclarations[].name`. The
  snake_case spellings (`system_instruction`, `function_declarations`) are read too. agy also sends `labels`
  and `generationConfig`, which are recorded in `Raw` and not interpreted.
- A function response whose object has a single string field is read as that string (agy:
  `{"output": "..."}`); anything else is its JSON.
- Response: `{candidates: [{content: {role: model, parts}, finishReason, index}], usageMetadata, modelVersion,
  responseId}`. A turn's parts are its text, then one `functionCall {id, name, args}` per tool call.
  `finishReason` is `STOP` for a turn that ends in function calls too, as the API does. Streamed as one SSE
  `data:` chunk per part, with `finishReason` and `usageMetadata` on the last. Errors are
  `{error: {code, message, status}}`.
- The model list is the Gemini shape (`models[]` of `{name: "models/<id>", baseModelId, supportedGenerationMethods,
  ...}`); `/v1/models` adds OpenAI's `{object: "list", data: [{id}]}`, since agy asks for both and which one it
  reads is not known.

## Differences from the Python server

- The binary listens on `-listen`, default `127.0.0.1:8000`, where the Python one took `--host` (default
  `0.0.0.0`) and `--port`: a container must pass `-listen 0.0.0.0:8000` to be reachable from the harness's.
- A harness that hangs up while its turn waits at a gate is a recorded error: the turn was used up but never
  delivered, and a retry would get the next one. (The Python server noticed only when the gate timed out.)
- Gates belong to the loaded scenario: `Load`/`Reset` opens and retires them, a `Release` before the `Load` does
  not carry over, and a `GET .../held` waiting across a `Load` gets 409.
- A scenario with data after its JSON object (a stray brace) is refused.
- A body that is not valid JSON, or a Gemini method other than the two above (or a stream without `alt=sse`), is
  a recorded error, not only a 400.
- `GET /_scripted/gates/{name}/held` and `WaitHeld` are new: the Python tests polled the recording.
- The gate timeout is `Options.GateTimeout` / `-gate-timeout`, not `SCRIPTED_MODEL_GATE_TIMEOUT`.
- In the chat adapter's `MessageText` a tool call's arguments appear as sent, not JSON-quoted again.

## Validated harnesses

`real_harness_test.go` (opt-in: `SLOP_BOT_TIER2=1 go test -run TestRealHarnesses ./internal/scriptedmodel`, which `slop-bot/tier2.sh` runs)
runs each CLI on this host for one scripted turn that runs `echo scripted-echo-7f3a` through the harness's shell
tool, then a final text reply, and asserts: the run exits 0, no errors were recorded, the script was used up, the
second request carries the echo's output as a tool result, and the harness printed the final text. Each harness
runs in a scratch directory with a clean environment (`HOME`, `PATH`, `TERM=xterm`) plus what is listed. It is
the seed of tier 2, whose conformance suite runs in `internal/harness/conformance/scripted` (through slop-bot's adapters, not bare CLIs). Validated on 2026-11-28:

| Harness | Version | Shell tool | Configuration |
| --- | --- | --- | --- |
| Claude Code | 2.1.332 | `Bash` `{command, description}` | `CLAUDE_CONFIG_DIR=<temp>`, `ANTHROPIC_BASE_URL=<url>`, `ANTHROPIC_API_KEY=sk-ant-scripted-model-not-a-real-key`, `DISABLE_AUTOUPDATER=1`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, `DISABLE_TELEMETRY=1`, `DISABLE_ERROR_REPORTING=1`, `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false`; `claude -p <prompt> --output-format stream-json --verbose --dangerously-skip-permissions` |
| Codex | 0.172.0 | `exec_command` `{cmd}` | `CODEX_HOME=<temp>`, `SCRIPTED_MODEL_API_KEY=<dummy>`; `codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -c model_provider=scripted -c model=scripted-model-1 -c 'model_providers.scripted={name="scripted",base_url="<url>/v1",env_key="SCRIPTED_MODEL_API_KEY",wire_api="responses"}' -c check_for_update_on_startup=false -c analytics.enabled=false -c feedback.enabled=false <prompt>` |
| pi | 1.4.0 | `bash` `{command}` | `PI_CODING_AGENT_DIR=<temp>` holding `models.json` (provider `scripted`: `baseUrl <url>/v1`, `api openai-completions`, `apiKey $SCRIPTED_MODEL_API_KEY`, model `scripted-model-1`, as `harness_e2e/harnesses/pi.py` writes it) and `settings.json`, `SCRIPTED_MODEL_API_KEY=<dummy>`, `PI_OFFLINE=1`, `PI_SKIP_VERSION_CHECK=1`, `PI_TELEMETRY=0`; `pi -p --provider scripted --model scripted-model-1 <prompt>` |
| agy | 1.5.18 | `run_command` `{CommandLine, toolSummary, WaitMsBeforeAsync}` | `HOME=<temp>`, `AGY_LLM_GATEWAY_URL=<url>`, `AGY_LLM_GATEWAY_API_KEY=<dummy>`; `agy -p <prompt> --dangerously-skip-permissions --settings '{"trustedWorkspaces":["<dir>"]}'`; model list `gemini-3.1-pro-preview` |

Each gets a config or home directory of its own, so none writes `~/.claude`, `~/.claude.json`, `~/.codex`
or `~/.gemini` (checked by file times after a run; pi's launcher still reads its install under
`~/.pi/agent/install`).

### Harness quirks seen

- **agy** asks for `gemini-3.1-pro-preview` (`GET /v1beta/models?pageSize=1000`, then `GET /v1/models`, then
  `POST /v1beta/models/gemini-3.1-pro-preview:streamGenerateContent?alt=sse`) even when the list is empty; it
  sends `Authorization: Bearer <key>`. It drops the `functionCall.id` when it replays the turn and adds its own
  `thoughtSignature` (base64 of `skip_thought_signature_validator`), so the API's signature check is not needed.
  It sends the tool's output back as `functionResponse.response.output` (with "Created At", exit code and
  "Command output:" lines around it). It retries a 500 eight times with backoff (about two and a half minutes), so
  an unexpected request makes a slow failure.
- **agy resumes a conversation cut off mid-turn** the next time it starts with the same `~/.gemini`: a run killed
  while retrying replayed its turn against the next run's script (and ran its tool calls). Hence the test's own
  `HOME`, which works because the gateway needs no Google sign-in.
- **Codex** warns that it will not create its PATH helpers under `/tmp` (the temp `CODEX_HOME`); harmless here.
  It warns that `scripted-model-1` has no model metadata, which is what keeps its tools plain (see
  `harness_e2e/harnesses/codex.py`).
- **Claude Code** in `-p` mode needs no onboarding or trust entry with its own `CLAUDE_CONFIG_DIR`; every request
  it made carried tools (none auxiliary).
