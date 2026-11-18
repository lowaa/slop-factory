// A long Remote Control session opens with its recent turns only and reads
// older ones as the person scrolls back, on a desktop and on a phone. Against
// a fake host (support/fake_conductor_host.ts) and the real api.
import type { APIRequestContext, Page } from "@playwright/test";

import { FakeConductorHost } from "./support/fake_conductor_host";
import { expect, test } from "./support/fixtures";
import { registerConductor } from "./support/api_helpers";

const TURNS = 30;
const FIRST_READ_TURNS = 10;
// Tall enough that ten turns fill the screen, so the first read is all there is.
const REPLY = "a long reply ".repeat(80);

async function seedLongSession(api: APIRequestContext, host: FakeConductorHost, conductorId: string): Promise<string> {
  const started = await api.post(`/api/conductors/${conductorId}/conversations`, {
    data: { message: "Turn 1 question", client_id: crypto.randomUUID() },
  });
  expect(started.status(), await started.text()).toBe(201);
  const { id } = (await started.json()) as { id: string };
  await host.ready(await host.nextItemOfType("start"));
  await host.nextItemOfType("message");
  for (let turn = 1; turn <= TURNS; turn++) {
    if (turn > 1) {
      const sent = await api.post(`/api/conductors/${conductorId}/conversations/${id}/messages`, {
        data: { text: `Turn ${turn} question`, client_id: crypto.randomUUID() },
      });
      expect(sent.status(), await sent.text()).toBe(201);
      await host.nextItemOfType("message");
    }
    await host.say(id, `Turn ${turn} answer: ${REPLY}`);
    await host.endTurn(id, turn);
  }
  return id;
}

// The older pages are held back a moment so the spinner can be seen.
async function slowOlderReads(page: Page, delayMs: number) {
  await page.route(/\/conversations\/[^/]+\/events\?/, async (route) => {
    const before = new URL(route.request().url()).searchParams.get("before");
    if (before !== null && before !== "0") await new Promise((r) => setTimeout(r, delayMs));
    await route.continue();
  });
}

for (const [label, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
] as const) {
  test.describe(label, () => {
    test.use({ viewport });

    test("opens with the recent turns only, and older turns load on scroll-up without moving the view", async ({ page, api, playwright }) => {
      const conductor = await registerConductor(api, `long-${label}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const id = await seedLongSession(api, host, conductor.id);
        await slowOlderReads(page, 600);

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
        const chat = page.getByRole("region", { name: "Session", exact: true });
        const transcript = chat.getByRole("list", { name: "Transcript" });
        const users = transcript.locator(".conversation__message--user");

        await expect(transcript).toContainText(`Turn ${TURNS} answer`);
        await expect(users).toHaveCount(FIRST_READ_TURNS);
        await expect(transcript).not.toContainText(`Turn ${TURNS - FIRST_READ_TURNS} question`);
        await expect(transcript).not.toContainText("Start of session");

        // Scrolled to the top, the oldest loaded turn is what the person is
        // reading; it must stay exactly there while older turns arrive above.
        const oldest = `Turn ${TURNS - FIRST_READ_TURNS + 1} question`;
        await transcript.evaluate((el) => (el.scrollTop = 0));
        const topOf = async () => {
          const box = (await transcript.getByText(oldest, { exact: true }).boundingBox())!;
          const list = (await transcript.boundingBox())!;
          return box.y - list.y;
        };
        await expect(transcript.getByText("Loading earlier messages…")).toBeVisible();
        const before = await topOf();

        await expect(users).toHaveCount(FIRST_READ_TURNS * 2);
        expect(Math.abs((await topOf()) - before), "the turn in view did not move").toBeLessThanOrEqual(2);
      } finally {
        await host.close();
      }
    });

    test("scrolled up, new output does not move the view and Jump to latest returns to the end", async ({ page, api, playwright }) => {
      const conductor = await registerConductor(api, `jump-${label}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const id = await seedLongSession(api, host, conductor.id);
        await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
        const chat = page.getByRole("region", { name: "Session", exact: true });
        const transcript = chat.getByRole("list", { name: "Transcript" });
        await expect(transcript).toContainText(`Turn ${TURNS} answer`);

        const scrollTop = () => transcript.evaluate((el) => el.scrollTop);
        await transcript.evaluate((el) => (el.scrollTop = el.scrollHeight / 2));
        const parked = await scrollTop();

        await host.say(id, "Fresh output while reading");
        await expect(chat.getByRole("button", { name: "Jump to latest" })).toBeVisible();
        expect(await scrollTop(), "the view stayed").toBe(parked);

        await chat.getByRole("button", { name: "Jump to latest" }).click();
        await expect(transcript).toContainText("Fresh output while reading");
        await expect(chat.getByRole("button", { name: "Jump to latest" })).toHaveCount(0);
        const atEnd = await transcript.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
        expect(atEnd).toBeLessThanOrEqual(24);
      } finally {
        await host.close();
      }
    });
  });
}
