import { expect, test } from "./support/fixtures";
import { newTicket, newAgent } from "./support/api_helpers";
import { claudeHookPayload, newSessionID } from "./support/env";

// The agent loop through real HTTP: Claude Code's http hook type POSTs the
// hook payload and reads the response as the hook's output, so these two
// endpoints *are* the loop. What the unit suite cannot show is that the
// wire shapes are the ones Claude Code actually reads.

test("session-start briefs the agent and binds the run", async ({ api }) => {
  const sessionID = newSessionID();
  const agent = await newAgent(api, `briefed-${Date.now()}`);

  const resp = await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  expect(resp.status(), await resp.text()).toBe(200);

  const body = await resp.json();
  const out = body.hookSpecificOutput;
  expect(out.hookEventName).toBe("SessionStart");
  expect(out.additionalContext).toContain(agent.name);
  // The working agreement reaches the agent as a file the workspace setup
  // writes; this context carries only what changes per session.
  expect(out.additionalContext).not.toContain("integration_lock");
});

test("a session-start retry is idempotent", async ({ api }) => {
  const agent = await newAgent(api, `retried-${Date.now()}`);
  const sessionID = `claude-${Date.now()}`;

  for (let i = 0; i < 2; i++) {
    const resp = await api.post("/hooks/session-start", {
      headers: agent.auth,
      data: { session_id: sessionID, cwd: "/srv/ws/1" },
    });
    expect(resp.status(), await resp.text()).toBe(200);
  }
});

test("stop blocks an agent that is still holding a ticket", async ({ api }) => {
  const sessionID = newSessionID();
  const agent = await newAgent(api, `holder-${Date.now()}`);
  const ticket = await newTicket(api, "Held work", { paths: ["server/internal/held/"] });

  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  await api.post("/api/claims", { headers: agent.auth, data: { ticket_id: ticket.id } });

  const resp = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  expect(resp.status(), await resp.text()).toBe(200);

  // hookSpecificOutput carries only the three documented fields.
  // `continue` and `systemMessage` are universal and sit beside it:
  // putting them inside made Claude Code discard the whole decision
  // without a word.
  const body = await resp.json();
  const out = body.hookSpecificOutput;
  expect(out.hookEventName).toBe("Stop");
  expect(out.decision).toBe("block");
  expect(out.reason).toContain(`#${ticket.id}`);
  expect(body.continue).toBe(true);
  expect(out.continue, "continue belongs beside the envelope, not inside it").toBeUndefined();
});

// The subagent guard. The Stop hook fires for subagents too, and without
// this an Explore subagent finishing a search gets told to claim tickets.
test("stop always lets a subagent stop", async ({ api }) => {
  const sessionID = newSessionID();
  const agent = await newAgent(api, `subagent-${Date.now()}`);
  const ticket = await newTicket(api, "Held work", { paths: ["server/internal/sub/"] });

  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  await api.post("/api/claims", { headers: agent.auth, data: { ticket_id: ticket.id } });

  const resp = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1", agent_id: "sub-1", agent_type: "Explore" },
  });
  expect(resp.status(), await resp.text()).toBe(200);
  expect((await resp.json()).hookSpecificOutput ?? null).toBeNull();
});

// Nothing sends an agent to the queue or to wait for work: a supervisor
// starts a run per ticket. An idle agent is let stop, claimable work or not,
// and the hook does not assign it.
test("stop lets an idle agent stop and does not claim work for it", async ({ api }) => {
  const sessionID = newSessionID();
  const agent = await newAgent(api, `idle-${Date.now()}`);
  const ticket = await newTicket(api, "Waiting to be taken", { paths: ["server/internal/waiting/"] });

  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });

  const resp = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  expect(resp.status(), await resp.text()).toBe(200);
  expect((await resp.json()).hookSpecificOutput ?? null).toBeNull();

  const after = await api.get(`/api/tickets/${ticket.id}`);
  expect((await after.json()).status).toBe("open");
});

test("the hook endpoints require a token", async ({ api }) => {
  const stop = await api.post("/hooks/stop", { data: { session_id: "x", cwd: "/tmp" } });
  expect(stop.status()).toBe(401);

  const start = await api.post("/hooks/session-start", { data: { session_id: "x", cwd: "/tmp" } });
  expect(start.status()).toBe(401);
});

// A superseded run keeps its terminal, its token and its Stop hook, so it
// goes on firing callbacks. It must be told to stop rather than kept
// working on the replacement run's ticket -- and its blocks must not count
// against the live session's loop breaker.
test("a stop from a superseded session is allowed to stop", async ({ api }) => {
  const agent = await newAgent(api, `superseded-${Date.now()}`);
  const oldSession = `claude-old-${Date.now()}`;
  const newSession = `claude-new-${Date.now()}`;

  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: oldSession, cwd: "/srv/ws/1" },
  });

  // The agent holds a ticket, so a *current* session would be blocked.
  const ticket = await newTicket(api, "Held across a restart", { paths: [`server/sup-${Date.now()}/`] });
  await api.post("/api/claims", { headers: agent.auth, data: { ticket_id: ticket.id } });

  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: newSession, cwd: "/srv/ws/1" },
  });

  const stale = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: oldSession, cwd: "/srv/ws/1" },
  });
  expect(stale.status(), await stale.text()).toBe(200);
  expect((await stale.json()).hookSpecificOutput ?? null).toBeNull();

  // The live session is still told to finish the ticket -- the lease
  // survived the restart, because leases belong to the agent.
  const live = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: newSession, cwd: "/srv/ws/1" },
  });
  const out = (await live.json()).hookSpecificOutput;
  expect(out.decision).toBe("block");
  expect(out.reason).toContain(`#${ticket.id}`);
});

// A hook payload is a contract slop-haus does not own. Claude Code sends
// considerably more than the four fields the handler reads, and adds to it
// over releases -- so the schema has to ignore what it does not recognise
// rather than reject the request.
//
// This is not defensive programming for its own sake. Rejecting unknown
// properties is huma's default, and it meant every real Stop hook came back
// 422 and the agent loop never ran once. Nothing caught it: the other tests
// here send exactly the fields the handler declares.
//
// So these post what a real Claude Code sent (testdata/claude, captured by
// harness_e2e/test.sh --capture-hook-payloads). The payloads above are
// hand-written: the minimal four-field shapes and the subagent case, which a
// real session in the capture does not produce.
test("the hooks accept the full payload Claude Code actually sends", async ({ api }) => {
  const agent = await newAgent(api, `payload-${Date.now()}`);
  const sessionID = newSessionID();

  const start = await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: claudeHookPayload("SessionStart", sessionID),
  });
  expect(start.status(), await start.text()).toBe(200);
  expect((await start.json()).hookSpecificOutput.additionalContext).toContain(agent.name);

  for (const event of ["Stop", "Stop_after_block"]) {
    const stop = await api.post("/hooks/stop", {
      headers: agent.auth,
      data: claudeHookPayload(event, sessionID),
    });
    expect(stop.status(), await stop.text()).toBe(200);
  }
});
