// Attempts end to end: a second attempt added from the ticket page is a
// shadow beside the promoted first, an unpromoted attempt handed over for
// review waits for a choice, each attempt has its own tab, conversation and
// worker cost, and the ticket's own view keeps its labels.
//
// Everything is driven through the API, the agents' MCP tools and the browser
// as a person and two agents would, against the stack's fake GitHub
// (support/fake_code_host.ts): a claim pins the base commit and makes the
// attempt's branches there, and an attempt is handed over through change_open
// and ticket_finish. The one setup shortcut left is in support/attempts.ts:
// the usage rows (as reports.spec.ts).
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import { newTicket, newAgent, type TestAgent } from "./support/api_helpers";
import { McpSession } from "./support/mcp";
import { recordAttemptUsage } from "./support/attempts";
import {
  attemptBranch,
  baseBranch,
  bodyOf,
  compareKey,
  comparison,
  createdBranches,
  githubRequests,
  HEAD_SHA,
  MAIN_SHA,
  openedPulls,
  pullNumber,
  pullUrl,
  REPO_PATH,
  removeFakeCodeHost,
  scriptTicket,
  useFakeCodeHost,
} from "./support/fake_code_host";

test.beforeEach(async ({ api }) => {
  await useFakeCodeHost(api);
});
test.afterEach(async ({ api }) => {
  await removeFakeCodeHost(api);
});

// A ticket whose scope is the repo, as an agent claim needs.
async function ticketUnderRepo(api: APIRequestContext, title: string): Promise<number> {
  return (await newTicket(api, title, { paths: [`${REPO_PATH}/`] })).id;
}

async function claim(api: APIRequestContext, agent: TestAgent) {
  const resp = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(resp.status(), await resp.text()).toBe(200);
  return resp.json();
}

function boardCard(page: Page, title: string, attemptText: string): Locator {
  return page.locator(".ticket-card").filter({ hasText: title }).filter({ hasText: attemptText });
}

function attemptTab(page: Page, n: number): Locator {
  return page.getByRole("navigation", { name: "Attempts" }).getByRole("link", { name: new RegExp(`^Attempt ${n}`) });
}

async function accentOf(locator: Locator): Promise<string> {
  return locator.evaluate((el) => getComputedStyle(el).getPropertyValue("--attempt-accent").trim());
}

// callTool is an agent's MCP tool call that must not be refused.
async function callTool(mcp: McpSession, name: string, args: Record<string, unknown>) {
  const result = await mcp.callTool(name, args);
  expect(result.isError, `${name}: ${result.text}`).toBe(false);
  return result;
}

// handOver is a shadow's finish: its change opened as a draft from its own
// branch, then the attempt handed over for review through it.
async function handOver(api: APIRequestContext, agent: TestAgent, ticket: number, attempt: number, title: string) {
  const mcp = await McpSession.open(api, agent.accessToken);
  await callTool(mcp, "change_open", { branch: attemptBranch(ticket, attempt), title, draft: true });
  await callTool(mcp, "ticket_finish", { pr_url: pullUrl(attempt), handoff: `attempt ${attempt} is ready`, request_review: true, reason: "an attempt for a person to choose between" });
}

// The form asks before it adds.
async function addOneAttempt(page: Page) {
  await page.getByRole("button", { name: "Add attempt", exact: true }).click();
  await page.getByRole("button", { name: "Confirm: add 1 attempt" }).click();
}

async function addShadow(page: Page, id: number) {
  await page.goto(`/tickets/${id}`);
  await addOneAttempt(page);
  await expect(attemptTab(page, 2)).toBeVisible();
}

// Attempt 1 is promoted and in progress under agent A; attempt 2 is a shadow
// added from the ticket page, taken by agent B and handed over for review.
async function twoAttempts(page: Page, api: APIRequestContext, request: APIRequestContext, title: string) {
  const id = await ticketUnderRepo(api, title);
  await scriptTicket(request, id, 2);

  const agentA = await newAgent(api, `attempts-a-${id}`);
  const agentB = await newAgent(api, `attempts-b-${id}`);
  const first = await claim(api, agentA);
  expect(first.ticket.id).toBe(id);

  await page.goto(`/tickets/${id}`);
  await addOneAttempt(page);
  await expect(attemptTab(page, 2)).toBeVisible();

  const second = await claim(api, agentB);
  expect(second.ticket.id).toBe(id);
  await handOver(api, agentB, id, 2, title);
  return id;
}

test("a second attempt is a shadow, and one handed over for review awaits a choice", async ({ page, api, request }) => {
  const title = `attempts-shadow-${Date.now()}`;
  const id = await ticketUnderRepo(api, title);
  await scriptTicket(request, id, 2);
  const agentA = await newAgent(api, `attempts-a-${id}`);
  await claim(api, agentA);

  // One attempt: the board shows one plain card and the page no tabs.
  await page.goto("/");
  await expect(page.locator(".ticket-card").filter({ hasText: title })).toHaveCount(1);
  await expect(page.locator(".ticket-card").filter({ hasText: title })).not.toContainText(/\d of \d attempts/);

  await page.goto(`/tickets/${id}`);
  await expect(page.getByRole("navigation", { name: "Attempts" })).toHaveCount(0);
  await addOneAttempt(page);
  await expect(attemptTab(page, 2)).toBeVisible();

  // The first attempt keeps its promotion; the new one is a shadow.
  const ticket = await (await api.get(`/api/tickets/${id}`)).json();
  expect(ticket.attempts).toMatchObject([
    { number: 1, status: "in_progress", promoted: true, shadow: false },
    { number: 2, status: "queued", promoted: false, shadow: true },
  ]);

  // Agent B takes the shadow, and the board shows a card for each attempt.
  const agentB = await newAgent(api, `attempts-b-${id}`);
  await claim(api, agentB);
  await page.goto("/");
  await expect(boardCard(page, title, "1 of 2 attempts")).toHaveCount(1);
  await expect(boardCard(page, title, "2 of 2 attempts")).toHaveCount(1);
  await expect(boardCard(page, title, "2 of 2 attempts")).not.toContainText("awaiting choice");

  // It hands the attempt over: nobody has chosen it yet.
  await handOver(api, agentB, id, 2, title);

  await page.reload();
  await expect(boardCard(page, title, "2 of 2 attempts")).toContainText("awaiting choice");
  await expect(boardCard(page, title, "1 of 2 attempts")).not.toContainText("awaiting choice");

  await boardCard(page, title, "2 of 2 attempts").getByRole("link").first().click();
  await expect(page).toHaveURL(new RegExp(`/tickets/${id}/attempts/2`));
  await expect(page.locator(".attempt-tab")).toContainText("awaiting choice");
  await attemptTab(page, 1).click();
  await expect(page.locator(".attempt-tab")).not.toContainText("awaiting choice");
});

test("an attempt's page says whose it is, and the ticket's own view keeps its labels", async ({ page, api, request }) => {
  const title = `attempts-identity-${Date.now()}`;
  const id = await twoAttempts(page, api, request, title);

  await page.goto(`/tickets/${id}`);
  await expect(page.getByText("Base", { exact: true })).toBeVisible();
  await expect(page.locator(".attempt-chip")).toHaveCount(0);
  await expect(page).toHaveTitle(`#${id} ${title} – slop-haus`);
  await expect(page.getByRole("button", { name: "Comment on the ticket (reaches every live attempt)" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Post comment" })).toHaveCount(0);

  await attemptTab(page, 1).click();
  await expect(page.locator(".attempt-chip")).toHaveText("attempt 1 · promoted");
  const accentOne = await accentOf(page.locator(".attempt-tab"));

  await attemptTab(page, 2).click();
  await expect(page).toHaveURL(new RegExp(`/tickets/${id}/attempts/2`));
  await expect(page.locator(".attempt-chip")).toHaveText("attempt 2 · shadow");
  await expect(page).toHaveTitle(`#${id} · attempt 2 – slop-haus`);

  // The header's fields read as the ticket's, not the attempt's.
  await expect(page.getByText("Ticket base", { exact: true })).toBeVisible();
  await expect(page.getByText("Base", { exact: true })).toHaveCount(0);

  await expect(page.locator(".attempt-tab")).toHaveClass(/attempt-accent-2/);
  await expect(page.locator(".attempt-chip")).toHaveClass(/attempt-accent-2/);
  const accentTwo = await accentOf(page.locator(".attempt-tab"));
  expect(accentTwo).not.toBe("");
  expect(accentTwo).not.toBe(accentOne);

  // Every action names the attempt it reaches.
  await expect(page.getByRole("button", { name: "Comment on attempt 2", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Approve attempt 2" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Request changes on attempt 2" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Approve", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Post comment" })).toHaveCount(0);
});

test("a comment on an attempt's tab reaches that attempt only", async ({ page, api, request }) => {
  const id = await twoAttempts(page, api, request, `attempts-blind-${Date.now()}`);
  const toAttemptTwo = "only for the shadow";
  const toTheTicket = "for the whole ticket";

  await page.goto(`/tickets/${id}/attempts/2`);
  await page.getByRole("textbox", { name: "Add a comment" }).fill(toAttemptTwo);
  await page.getByRole("button", { name: "Comment on attempt 2", exact: true }).click();
  await expect(page.locator(".attempt-tab")).toContainText(toAttemptTwo);

  await attemptTab(page, 1).click();
  await expect(page.locator(".attempt-chip")).toHaveText("attempt 1 · promoted");
  await expect(page.locator(".attempt-tab")).not.toContainText(toAttemptTwo);
  // The other attempts' conversations stay hidden until asked for.
  await expect(page.getByText(/hidden so they do not anchor/)).toBeVisible();

  await page.getByRole("navigation", { name: "Attempts" }).getByRole("link", { name: "Ticket", exact: true }).click();
  await expect(page.locator(".attempt-chip")).toHaveCount(0);
  await expect(page.locator(".ticket-detail__comments").getByRole("listitem").filter({ hasText: toAttemptTwo })).toHaveCount(0);

  await page.getByRole("textbox", { name: "Add a comment" }).fill(toTheTicket);
  await page.getByRole("button", { name: "Comment on the ticket (reaches every live attempt)" }).click();
  await expect(page.locator(".ticket-detail__comments").getByRole("listitem").filter({ hasText: toTheTicket })).toBeVisible();

  // The ticket's comment is the ticket's own, and attempt 2's stays attempt 2's.
  const { comments } = (await (await api.get(`/api/tickets/${id}/comments`)).json()) as {
    comments: { body: string; attempt_number: number | null }[];
  };
  expect(comments.find((c) => c.body === toAttemptTwo)?.attempt_number).toBe(2);
  const ticketComment = comments.find((c) => c.body === toTheTicket);
  expect(ticketComment, JSON.stringify(comments)).toBeDefined();
  expect(ticketComment?.attempt_number ?? null).toBeNull();
  await attemptTab(page, 1).click();
  await expect(page.locator(".attempt-chip")).toHaveText("attempt 1 · promoted");
  await expect(page.locator(".attempt-tab")).not.toContainText(toTheTicket);
});

test("each attempt shows its own worker cost, matching its card, and Reports adds them up", async ({ page, api, request }) => {
  const title = `attempts-cost-${Date.now()}`;
  const id = await twoAttempts(page, api, request, title);
  await recordAttemptUsage({ key: `cost-1-${id}`, ticket: id, attempt: 1, costMicros: 3_000_000 });
  await recordAttemptUsage({ key: `cost-2-${id}`, ticket: id, attempt: 2, costMicros: 1_250_000 });

  await page.goto("/");
  await expect(boardCard(page, title, "1 of 2 attempts")).toContainText("$3.00 estimated");
  await expect(boardCard(page, title, "2 of 2 attempts")).toContainText("$1.25 estimated");

  await page.goto(`/tickets/${id}/attempts/1`);
  await expect(page.locator(".attempt-tab__cost")).toHaveText("$3.00 estimated");
  await attemptTab(page, 2).click();
  await expect(page.locator(".attempt-chip")).toHaveText("attempt 2 · shadow");
  await expect(page.locator(".attempt-tab__cost")).toHaveText("$1.25 estimated");

  // Reports shows only the ticket's total, the sum of its attempts.
  await page.goto("/reports");
  const section = page.getByRole("region", { name: "LLM usage" });
  await expect(section.getByText("$4.25 estimated").first()).toBeVisible();
});

async function expectRefused(mcp: McpSession, name: string, args: Record<string, unknown>, reason: RegExp) {
  const result = await mcp.callTool(name, args);
  expect(result.isError, `${name} was not refused: ${result.text}`).toBe(true);
  expect(result.text).toMatch(reason);
}

test("a claim pins the base commit from the code host and makes each attempt's branch once", async ({ page, api, request }) => {
  const id = await ticketUnderRepo(api, `attempts-branches-${Date.now()}`);
  await scriptTicket(request, id, 2);

  const agentA = await newAgent(api, `attempts-a-${id}`);
  await claim(api, agentA);
  const pinned = await (await api.get(`/api/tickets/${id}`)).json();
  expect(pinned.base_commit).toBe(MAIN_SHA);
  const afterOne = await githubRequests(request);
  expect(afterOne.some((r) => r.method === "GET" && r.path.endsWith("/commits/main"))).toBe(true);
  // The promoted attempt opens onto main, so only its own branch is made.
  expect(createdBranches(afterOne)).toEqual([{ branch: attemptBranch(id, 1), sha: MAIN_SHA }]);

  // Reading the held attempt again finds its branch made, and makes nothing.
  await callTool(await McpSession.open(api, agentA.accessToken), "ticket_get", { ticket_id: id });
  expect(createdBranches(await githubRequests(request))).toHaveLength(1);

  // A shadow gets the ticket's base branch too, at the same pinned commit.
  await addShadow(page, id);
  const agentB = await newAgent(api, `attempts-b-${id}`);
  await claim(api, agentB);
  expect(createdBranches(await githubRequests(request))).toEqual([
    { branch: attemptBranch(id, 1), sha: MAIN_SHA },
    { branch: baseBranch(id), sha: MAIN_SHA },
    { branch: attemptBranch(id, 2), sha: MAIN_SHA },
  ]);
  await callTool(await McpSession.open(api, agentB.accessToken), "ticket_get", { ticket_id: id });
  expect(createdBranches(await githubRequests(request))).toHaveLength(3);
});

test("the promoted attempt's change targets main and merges; a shadow's is a draft on the ticket's base branch", async ({ page, api, request }) => {
  const title = `attempts-open-${Date.now()}`;
  const id = await ticketUnderRepo(api, title);
  await scriptTicket(request, id, 2);
  const agentA = await newAgent(api, `attempts-a-${id}`);
  await claim(api, agentA);
  await addShadow(page, id);
  const agentB = await newAgent(api, `attempts-b-${id}`);
  await claim(api, agentB);

  const shadow = await McpSession.open(api, agentB.accessToken);
  // Not a draft is the choice a person makes, so it is refused for a shadow.
  await expectRefused(shadow, "change_open", { branch: attemptBranch(id, 2), title, draft: false }, /draft false refused/);
  expect(openedPulls(await githubRequests(request))).toEqual([]);
  await callTool(shadow, "change_open", { branch: attemptBranch(id, 2), title, draft: true });

  const promoted = await McpSession.open(api, agentA.accessToken);
  await callTool(promoted, "change_open", { branch: attemptBranch(id, 1), title });

  const opened = openedPulls(await githubRequests(request));
  expect(opened).toMatchObject([
    { head: attemptBranch(id, 2), base: baseBranch(id), draft: true },
    { head: attemptBranch(id, 1), base: "main", draft: false },
  ]);

  // The promoted attempt's change merges through the host, for the head the
  // checks were read on.
  await callTool(promoted, "integration_lock", {});
  const checks = await callTool(promoted, "change_checks", { head_sha: HEAD_SHA(1) });
  expect(checks.text).toContain("are success");
  const merged = await callTool(promoted, "change_merge", { head_sha: HEAD_SHA(1) });
  expect(merged.text).toContain(`Merged ${pullUrl(1)}`);
  const merge = (await githubRequests(request)).find((r) => r.method === "PUT" && r.path.endsWith(`/pulls/${pullNumber(1)}/merge`));
  expect(merge, "the merge reached the host").toBeDefined();
  expect(bodyOf(merge!)).toMatchObject({ sha: HEAD_SHA(1), merge_method: "merge" });
});

test("change_open refuses a branch that is not the attempt's own", async ({ api, request }) => {
  const title = `attempts-own-branch-${Date.now()}`;
  const id = await ticketUnderRepo(api, title);
  await scriptTicket(request, id, 1);
  const agent = await newAgent(api, `attempts-a-${id}`);
  await claim(api, agent);
  const mcp = await McpSession.open(api, agent.accessToken);

  await expectRefused(mcp, "change_open", { branch: attemptBranch(id, 2), title }, new RegExp(`this attempt's branch is ${attemptBranch(id, 1)}, not ${attemptBranch(id, 2)}`));
  await expectRefused(mcp, "change_open", { branch: "my-own-branch", title }, new RegExp(`this attempt's branch is ${attemptBranch(id, 1)}`));
  expect(openedPulls(await githubRequests(request))).toEqual([]);

  await callTool(mcp, "change_open", { branch: attemptBranch(id, 1), title });
  expect(openedPulls(await githubRequests(request))).toHaveLength(1);
});

test("change_open refuses a shadow's branch that took in main past the base commit", async ({ page, api, request }) => {
  const title = `attempts-took-main-${Date.now()}`;
  const id = await ticketUnderRepo(api, title);
  const moved = "2222222222222222222222222222222222222222";
  const head = attemptBranch(id, 2);
  // The shadow's branch was cut from a commit main reached after the base
  // commit: the comparison's merge base is not the base commit, and is ahead of it.
  await scriptTicket(request, id, 2, {
    [compareKey("main", head)]: comparison(moved),
    [compareKey(MAIN_SHA, moved)]: { body: { status: "ahead" } },
  });
  const agentA = await newAgent(api, `attempts-a-${id}`);
  await claim(api, agentA);
  await addShadow(page, id);
  const agentB = await newAgent(api, `attempts-b-${id}`);
  await claim(api, agentB);
  const mcp = await McpSession.open(api, agentB.accessToken);

  await expectRefused(mcp, "change_open", { branch: head, title, draft: true }, /contains commits from the main branch made after the ticket's base commit/);
  expect(openedPulls(await githubRequests(request))).toEqual([]);
});
