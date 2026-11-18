// Console proposals end to end, on a phone: a console agent proposes a human
// action over MCP, the person decides on the Approvals tab, and only an
// approval touches the ticket. The Go tests prove each rule against the
// database; what only this suite can show is the whole path through the
// deployed stack and a real browser at the width the person actually uses.
import type { APIRequestContext } from "@playwright/test";

import { API_BASE_URL } from "./support/env";
import { expect, test } from "./support/fixtures";
import { fileTicket } from "./support/api_helpers";
import { McpSession } from "./support/mcp";
import { credentialAgentViaCodeFlow, credentialConsoleViaCodeFlow } from "./support/oauth";

const PHONE = { width: 390, height: 844 };

test.use({ viewport: PHONE });

interface Agent {
  id: string;
  auth: { Authorization: string };
  mcp: McpSession;
}

async function openAgent(api: APIRequestContext, name: string): Promise<Agent> {
  const { agentId, accessToken } = await credentialAgentViaCodeFlow(api, name);
  return {
    id: agentId,
    auth: { Authorization: `Bearer ${accessToken}` },
    mcp: await McpSession.open(api, accessToken),
  };
}

async function openConsole(api: APIRequestContext, name: string): Promise<Agent> {
  const { agentId, accessToken } = await credentialConsoleViaCodeFlow(api, name);
  return {
    id: agentId,
    auth: { Authorization: `Bearer ${accessToken}` },
    mcp: await McpSession.open(api, accessToken),
  };
}

// A ticket an agent has asked a question on, so it sits in discussion with
// nobody holding it.
async function discussionTicket(
  api: APIRequestContext,
  asker: Agent,
  title: string,
): Promise<number> {
  const id = await fileTicket(api, title);
  const claim = await api.post("/api/claims", {
    headers: asker.auth,
    data: { ticket_id: id },
  });
  expect(claim.status(), await claim.text()).toBe(200);
  const asked = await asker.mcp.callTool("ticket_discuss", {
    question: "Which way?",
    handoff: "Nothing built.",
  });
  expect(asked.isError, asked.text).toBe(false);
  return id;
}

async function ticketStatus(
  api: APIRequestContext,
  id: number,
): Promise<string> {
  const resp = await api.get(`/api/tickets/${id}`);
  expect(resp.status(), await resp.text()).toBe(200);
  return (await resp.json()).status;
}

async function comments(api: APIRequestContext, id: number) {
  const resp = await api.get(`/api/tickets/${id}/comments`);
  expect(resp.status(), await resp.text()).toBe(200);
  return (await resp.json()).comments as {
    author_agent_id: string | null;
    author_user_id: string | null;
    body: string;
  }[];
}

async function proposalsOf(console: Agent, status?: string): Promise<string> {
  const listed = await console.mcp.callTool(
    "list_proposals",
    status ? { status } : {},
  );
  expect(listed.isError, listed.text).toBe(false);
  return listed.text;
}

test("approving a console's answer takes the ticket out of discussion and the asking worker can claim it again", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const asker = await openAgent(api, `asker-${stamp}`);
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await discussionTicket(api, asker, `approve-${stamp}`);

  const answer = "Flat, and say so in the README.";
  const proposed = await consoleAgent.mcp.callTool(
    "propose_discussion_answer",
    { ticket_id: id, body: answer },
  );
  expect(proposed.isError, proposed.text).toBe(false);
  expect(proposed.text).toContain("Nothing has changed yet");
  expect(await ticketStatus(api, id)).toBe("discussion");

  await page.goto("/approvals");
  const card = page.getByRole("group", { name: `Proposal on #${id}` });
  await expect(card).toBeVisible();
  await expect(card.getByText(answer)).toBeVisible();

  // The phone's tabs are in the menu; the badge is on the button.
  await expect(
    page.getByRole("button", { name: /^Menu/ }).getByRole("status"),
  ).toHaveAccessibleName("1 waiting for approval");

  // Finger-sized, and not running off the side.
  const approve = card.getByRole("button", { name: "Approve", exact: true });
  const box = (await approve.boundingBox())!;
  expect(box.height).toBeGreaterThanOrEqual(44);
  const overflow = await page.evaluate(() => {
    const main = document.querySelector(".app__main")!;
    return (
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth +
      (main.scrollWidth - main.clientWidth)
    );
  });
  expect(overflow, "the approvals tab should not scroll sideways").toBe(0);

  await approve.click();
  await expect(page.getByText("Nothing waiting for you.")).toBeVisible();
  await expect(
    page.getByRole("button", { name: /^Menu/ }).getByRole("status"),
  ).toHaveCount(0);

  expect(await ticketStatus(api, id)).toBe("open");
  const posted = (await comments(api, id)).find((c) => c.body.includes(answer));
  expect(posted, "the answer is posted").toBeDefined();
  expect(posted!.author_user_id, "as the person, not the agent").not.toBeNull();
  expect(posted!.author_agent_id).toBeNull();
  expect(posted!.body).toContain(`Drafted by console-${stamp}`);

  const claimed = await asker.mcp.callTool("ticket_next", { ticket_id: id });
  expect(claimed.isError, claimed.text).toBe(false);
  expect(claimed.text).toContain(`#${id}`);

  expect(await proposalsOf(consoleAgent, "approved")).toContain(`#${id}`);
});

test("rejecting with a reason leaves the ticket alone and the console reads the reason", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const asker = await openAgent(api, `asker-${stamp}`);
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await discussionTicket(api, asker, `reject-${stamp}`);

  const proposed = await consoleAgent.mcp.callTool(
    "propose_discussion_answer",
    { ticket_id: id, body: "Nested." },
  );
  expect(proposed.isError, proposed.text).toBe(false);

  await page.goto("/approvals");
  const card = page.getByRole("group", { name: `Proposal on #${id}` });
  await card.getByRole("button", { name: "Reject", exact: true }).click();
  await card.getByLabel(/Reason/).fill("We decided flat in chat.");
  await card.getByRole("button", { name: "Reject", exact: true }).click();
  await expect(page.getByText("Nothing waiting for you.")).toBeVisible();

  expect(await ticketStatus(api, id)).toBe("discussion");
  expect(
    (await comments(api, id)).some((c) => c.body.includes("Nested.")),
  ).toBe(false);

  const listed = await proposalsOf(consoleAgent, "rejected");
  expect(listed).toContain(`#${id}`);
  expect(listed).toContain("We decided flat in chat.");
});

test("a proposal whose ticket changed offers only dismiss, and approving it is refused", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await fileTicket(api, `stale-${stamp}`, "preparing");

  const proposed = await consoleAgent.mcp.callTool("propose_ready", {
    ticket_id: id,
  });
  expect(proposed.isError, proposed.text).toBe(false);

  // The person edits it before deciding. Still in preparing, so the ticket's
  // own rules would accept the move: only the proposal's basis says no.
  const edited = await api.post(`/api/tickets/${id}/revisions`, {
    data: { title: `stale-edited-${stamp}` },
  });
  expect(edited.status(), await edited.text()).toBe(200);

  await page.goto("/approvals");
  const card = page.getByRole("group", { name: `Proposal on #${id}` });
  await expect(card.getByRole("note")).toContainText("The ticket has changed");
  await expect(
    card.getByRole("button", { name: "Approve", exact: true }),
  ).toHaveCount(0);

  // Nothing in the page's way: the server refuses it too.
  const pending = await (await api.get("/api/proposals?state=pending")).json();
  const proposalId = pending.proposals.find(
    (p: { ticket_id: number }) => p.ticket_id === id,
  ).id;
  const refused = await api.post(`/api/proposals/${proposalId}/approve`);
  expect(refused.status()).toBe(409);
  expect(await ticketStatus(api, id)).toBe("preparing");

  await page.reload();
  await expect(page.getByText("Nothing waiting for you.")).toBeVisible();
  const listed = await proposalsOf(consoleAgent);
  expect(listed).toContain(`#${id}`);
  expect(listed).not.toContain("pending");
});

test("a worker that is not a console cannot propose, and no agent token can approve", async ({
  api,
  playwright,
}) => {
  const stamp = Date.now();
  const worker = await openAgent(api, `worker-${stamp}`);
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await fileTicket(api, `refused-${stamp}`, "preparing");

  const refused = await worker.mcp.callTool("propose_ready", { ticket_id: id });
  expect(refused.isError).toBe(true);
  const none = await (await api.get("/api/proposals")).json();
  expect(none.proposals).toHaveLength(0);

  const proposed = await consoleAgent.mcp.callTool("propose_ready", {
    ticket_id: id,
  });
  expect(proposed.isError, proposed.text).toBe(false);
  const { proposals } = await (
    await api.get("/api/proposals?state=pending")
  ).json();
  const proposalId = proposals[0].id;

  // A bare context: nothing but the agent's own token, no session cookie.
  const bare = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    for (const agent of [worker, consoleAgent]) {
      const resp = await bare.post(`/api/proposals/${proposalId}/approve`, {
        headers: agent.auth,
      });
      expect(
        resp.status(),
        "an agent token must not approve",
      ).toBeGreaterThanOrEqual(401);
      expect(resp.status()).toBeLessThan(500);
    }
  } finally {
    await bare.dispose();
  }
  expect(await ticketStatus(api, id)).toBe("preparing");
});

test("approving a ready proposal puts a preparing ticket in the queue", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await fileTicket(api, `ready-${stamp}`, "preparing");
  const proposed = await consoleAgent.mcp.callTool("propose_ready", {
    ticket_id: id,
  });
  expect(proposed.isError, proposed.text).toBe(false);
  expect(await ticketStatus(api, id)).toBe("preparing");

  await page.goto("/approvals");
  const card = page.getByRole("group", { name: `Proposal on #${id}` });
  await card.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(page.getByText("Nothing waiting for you.")).toBeVisible();

  expect(await ticketStatus(api, id)).toBe("open");
  expect(await proposalsOf(consoleAgent, "approved")).toContain(`#${id}`);
});

test("approving a park proposal parks the ticket, with the reason as the person's comment", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const consoleAgent = await openConsole(api, `console-${stamp}`);
  const id = await fileTicket(api, `park-${stamp}`);
  const proposed = await consoleAgent.mcp.callTool("propose_status", {
    ticket_id: id,
    status: "parked",
    reason: "Not this quarter.",
  });
  expect(proposed.isError, proposed.text).toBe(false);
  expect(await ticketStatus(api, id)).toBe("open");

  await page.goto("/approvals");
  const card = page.getByRole("group", { name: `Proposal on #${id}` });
  await card.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(page.getByText("Nothing waiting for you.")).toBeVisible();

  expect(await ticketStatus(api, id)).toBe("parked");
  const posted = (await comments(api, id)).find((c) =>
    c.body.includes("Not this quarter."),
  );
  expect(posted?.author_user_id).not.toBeNull();
  expect(posted?.body).toContain(`Drafted by console-${stamp}`);
});
