// Styling assertions, which live here rather than in the component suite
// because vitest runs with `css: false` -- jsdom never applies a
// stylesheet, so nothing there can see what a rule actually did.
//
// These are deliberately few. Pinning colours wholesale would make every
// design change a test change; what is pinned is the small set of things
// that broke silently once already.
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { defaultStyleChecker } from "@/support/default_style";
import { execute } from "@/support/db";
import { expect, test } from "@/support/fixtures";
import { openNewTicketForm } from "@/support/widgets";

test("the app is dark, not the browser default", async ({ page }) => {
  await page.goto("/");

  const background = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const [r, g, b] = background.match(/\d+/g)!.map(Number);
  expect(r + g + b, `body background is ${background}`).toBeLessThan(120);
});

// The bug this exists for: new_ticket_form.scss styled `button` without
// scoping it, and the Priority picker's trigger is a <button>, so the form
// painted the widget solid accent -- a filled blue bar where a dropdown
// should be. Every component test passed; it was only visible in a
// screenshot.
test("a picker inside a form is not repainted by the form's own button styling", async ({ page }) => {
  await page.goto("/");
  await openNewTicketForm(page);

  const background = (locator: ReturnType<typeof page.getByRole>) =>
    locator.evaluate((el) => getComputedStyle(el).backgroundColor);

  const submit = await background(page.getByRole("button", { name: "Preparing" }));
  const picker = await background(page.getByRole("combobox", { name: "Priority" }));

  expect(picker, "the dropdown trigger must not wear the submit button's fill").not.toBe(submit);
});

// The panel is drawn over the page, so anything translucent puts the
// content behind it -- and the body's grid -- straight through the option
// labels.
test("an open dropdown panel is opaque", async ({ page }) => {
  await page.goto("/");
  await openNewTicketForm(page);

  await page.getByRole("combobox", { name: "Priority" }).click();
  const panel = page.getByRole("listbox");
  await expect(panel).toBeVisible();

  const background = await panel.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(background, `panel background is ${background}`).not.toMatch(/rgba\(.*,\s*0?\.\d+\)/);
});

async function createTicket(api: APIRequestContext, data: { title: string; body?: string }): Promise<{ id: number }> {
  const resp = await api.post("/api/tickets", { data });
  expect(resp.status(), await resp.text()).toBe(201);
  return resp.json();
}

// What makes the controls render: a ticket with a comment, a handoff and
// blockers, and a worker. cleanDatabase empties everything else, and an
// empty panel has few controls to check.
async function seedTicketWithEverything(api: APIRequestContext): Promise<number> {
  const ticket = await createTicket(api, { title: "styled ticket", body: "body" });
  for (const title of ["blocker one", "blocker two"]) {
    const blocker = await createTicket(api, { title });
    const resp = await api.post(`/api/tickets/${ticket.id}/blockers`, { data: { blocker_id: blocker.id } });
    expect(resp.status(), await resp.text()).toBe(200);
  }
  const comment = await api.post(`/api/tickets/${ticket.id}/comments`, { data: { body: "a comment" } });
  expect(comment.status(), await comment.text()).toBe(201);

  const agentResp = await api.post("/api/agents", { data: { name: "styled-worker" } });
  expect(agentResp.status(), await agentResp.text()).toBe(201);
  const agent = await agentResp.json();
  // Straight to the table, as layout.spec.ts does: only an agent's
  // ticket_discuss writes a handoff over the wire.
  for (const body of ["first handoff", "second handoff"]) {
    await execute(
      "INSERT INTO agent_contexts (tenant_id, ticket_id, attempt_number, agent_id, body) SELECT tenant_id, $1, 1, id, $3 FROM agents WHERE id = $2",
      [ticket.id, agent.id, body],
    );
  }
  return ticket.id;
}

interface Surface {
  name: string;
  open: (page: Page, ticketId: number) => Promise<void>;
  // Something that only shows once the page has loaded its data: the scan
  // runs once, so a control rendered after it would go unchecked.
  ready: (page: Page) => Locator;
}

const tab = (name: string, path: string, ready: (page: Page) => Locator): Surface => ({
  name,
  open: async (page) => {
    await page.goto(path);
  },
  ready,
});

const heading = (name: string | RegExp) => (page: Page) => page.getByRole("heading", { name });

// Controls that still look native because the ticket named here has not
// landed. An entry that no longer matches anything fails the test, so each
// is removed as its ticket merges.
interface KnownDefect {
  page: string;
  control: RegExp;
  ticket: string;
}
const KNOWN_DEFAULT_STYLED: KnownDefect[] = [];

const SURFACES: Surface[] = [
  tab("board", "/", (page) => page.getByText("styled ticket")),
  {
    name: "board with the new-ticket form open",
    open: async (page) => {
      await page.goto("/");
      await openNewTicketForm(page);
    },
    ready: (page) => page.getByText("styled ticket"),
  },
  {
    name: "ticket detail",
    open: async (page, ticketId) => {
      await page.goto(`/tickets/${ticketId}`);
    },
    ready: (page) => page.getByText("a comment"),
  },
  {
    name: "ticket detail, agent context history",
    open: async (page, ticketId) => {
      await page.goto(`/tickets/${ticketId}`);
      await page.getByRole("tab", { name: /Agent context history/ }).click();
    },
    ready: (page) => page.getByRole("button", { name: "Expand all" }),
  },
  tab("blocked", "/blocked", (page) => page.getByText("styled ticket")),
  tab("unreviewed", "/unreviewed", (page) => page.getByRole("navigation")),
  tab("remote control", "/remote-control", (page) => page.getByRole("navigation")),
  tab("reports", "/reports", (page) => page.getByLabel("Priority")),
  tab("workers", "/workers", (page) => page.getByText("styled-worker")),
  tab("workspace", "/workspace", heading("Workspace")),
  tab("git", "/git", heading("GitHub webhooks")),
  tab("intake", "/intake", heading("Intake sources")),
  tab("members", "/members", heading("Invite someone")),
  tab("account", "/account", heading("Account")),
];

// new_ticket_form.scss once repainted a control it didn't own, and the
// per-component Storybook check cannot see one component's CSS reaching
// another's, so this runs the same comparison over the whole app.
test("no visible control on the main pages uses the browser's default look", async ({ page, api }) => {
  const ticketId = await seedTicketWithEverything(api);

  const failures: string[] = [];
  const stillNeeded = new Set<KnownDefect>();
  for (const surface of SURFACES) {
    await surface.open(page, ticketId);
    await expect(surface.ready(page).first(), `${surface.name} loaded`).toBeVisible();
    for (const { description } of await defaultStyleChecker.find(page)) {
      const known = KNOWN_DEFAULT_STYLED.find((k) => k.page === surface.name && k.control.test(description));
      if (known) {
        stillNeeded.add(known);
        continue;
      }
      failures.push(`[${surface.name}] ${description}`);
    }
  }

  for (const known of KNOWN_DEFAULT_STYLED) {
    if (!stillNeeded.has(known)) {
      failures.push(`[${known.page}] opt-out for ${known.ticket} matches nothing: remove it`);
    }
  }

  expect(failures, `controls with the browser's default look:\n${failures.join("\n")}`).toEqual([]);
});
