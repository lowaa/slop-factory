// Close as merged end to end: a change merged on the code host by someone
// slop-haus does not act on leaves its ticket in review, and the workspace
// owner closes it from the ticket page on the host's word. Driven as an agent
// (change_open, ticket_finish) and a person through the stack's fake GitHub
// (support/fake_code_host.ts).
import type { APIRequestContext } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import { newTicket, newAgent } from "./support/api_helpers";
import { McpSession } from "./support/mcp";
import {
  attemptBranch,
  compareKey,
  HEAD_SHA,
  MERGE_SHA,
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

// inReview is a ticket whose agent handed its change over for review.
async function inReview(api: APIRequestContext, request: APIRequestContext, title: string): Promise<number> {
  const id = (await newTicket(api, title, { paths: [`${REPO_PATH}/`] })).id;
  await scriptTicket(request, id, 1);
  const agent = await newAgent(api, `merged-elsewhere-${id}`);
  const claimed = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(claimed.status(), await claimed.text()).toBe(200);
  const mcp = await McpSession.open(api, agent.accessToken);
  const opened = await mcp.callTool("change_open", { branch: attemptBranch(id, 1), title, draft: true });
  expect(opened.isError, opened.text).toBe(false);
  const finished = await mcp.callTool("ticket_finish", {
    pr_url: pullUrl(1),
    handoff: "ready",
    request_review: true,
    reason: "a change for a person to read",
  });
  expect(finished.isError, finished.text).toBe(false);
  return id;
}

test("the owner closes a ticket whose change was merged elsewhere, after confirming it was never approved", async ({ page, api, request }) => {
  const title = `close-as-merged-${Date.now()}`;
  const id = await inReview(api, request, title);
  // The change is then merged by hand, on the host, into main.
  await scriptTicket(request, id, 1, {
    [`pull:${pullNumber(1)}`]: {
      body: {
        number: pullNumber(1),
        node_id: "PR_1",
        draft: false,
        merged: true,
        merge_commit_sha: MERGE_SHA,
        merged_by: { login: "merge-bot" },
        head: { sha: HEAD_SHA(1) },
        base: { ref: "main" },
      },
    },
    [compareKey(MERGE_SHA, "main")]: { body: { status: "ahead" } },
  });

  await page.goto(`/tickets/${id}`);
  const panel = page.getByRole("region", { name: "Merged outside slop-haus" });
  await expect(panel).toContainText("Checks on that head passed.");
  await expect(panel).toContainText("is on main now");
  await panel.getByRole("button", { name: "Close as merged" }).click();

  const confirm = page.getByRole("alertdialog", { name: "Confirm close as merged" });
  await expect(confirm).toContainText("was never approved");
  await confirm.getByRole("button", { name: "Close as merged anyway" }).click();

  await expect(page.getByRole("region", { name: "Merged outside slop-haus" })).toHaveCount(0);
  const done = await api.get(`/api/tickets/${id}`);
  expect((await done.json()).status).toBe("done");
});

test("a change the host does not show merged cannot be closed as merged", async ({ page, api, request }) => {
  const id = await inReview(api, request, `close-as-merged-open-${Date.now()}`);

  await page.goto(`/tickets/${id}`);
  await expect(page.getByRole("heading", { name: new RegExp(`#${id}`) })).toBeVisible();
  await expect(page.getByRole("button", { name: "Close as merged" })).toHaveCount(0);

  const refused = await api.post(`/api/tickets/${id}/close_as_merged`, { data: { confirm: true } });
  expect(refused.status()).toBe(409);
});
