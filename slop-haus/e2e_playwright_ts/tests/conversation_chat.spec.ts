// A Remote Control session end to end, against a fake host: the browser, the
// real api, and a host that does only what the test tells it
// (support/fake_conductor_host.ts). The Go and component tests prove each rule;
// this shows the path between them, on a desktop and on a phone.
import type { APIRequestContext, Page } from "@playwright/test";

import { rejectAllConsent } from "@/support/consent";
import { registerConductor } from "./support/api_helpers";
import { FakeConductorHost } from "./support/fake_conductor_host";
import { expect, test } from "./support/fixtures";
import { McpSession } from "./support/mcp";
import {
  DESKTOP,
  EXPLAINED,
  type Device,
  fact,
  openSessionMenu,
  openSessions,
  PHONE,
  setExplained,
  twoSessions,
} from "./support/remote_control_chat";

// A session started from the page and handed its first message, with the
// host's side of it already answered: what the focused tests below build on.
async function startSession(page: Page, host: FakeConductorHost, name: string, device: Device) {
  await page.goto("/remote-control");
  const machine = page.getByRole("region", { name });
  await openSessions(page);
  await machine.getByRole("button", { name: "+ New session" }).click();
  await page.getByLabel("Message").fill("What does this change do?");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page).toHaveURL(/session=/);
  const chat = page.getByRole("region", { name: "Session" });

  // The host is asked to start in its working copy as it is: nothing to
  // check out, nothing to resume.
  const start = await host.nextItemOfType("start");
  expect(start.resume ?? false).toBe(false);
  await host.ready(start);
  await openSessionMenu(page, device);
  await expect(fact(chat, "State")).toHaveText("Connected");
  await expect(fact(chat, "Host")).toHaveText("Online");

  // The first message came with the start, ahead of anything else.
  const first = await host.nextItemOfType("message");
  expect(first.text).toBe("What does this change do?");
  return { machine, chat, start, first };
}

// Runs body against a registered machine with a fake host, closing the host
// whichever way the body ends.
async function withHost(
  api: APIRequestContext,
  playwright: Parameters<typeof FakeConductorHost.open>[0],
  name: string,
  body: (host: FakeConductorHost, conductor: { id: string; accessToken: string }) => Promise<void>,
): Promise<void> {
  const conductor = await registerConductor(api, name);
  const host = await FakeConductorHost.open(playwright, conductor.accessToken);
  try {
    await body(host, conductor);
  } finally {
    await host.close();
  }
}



// The serial suite shares one signed-in user: the explanation is already
// dismissed unless a test is about it.
test.beforeEach(async ({ api }) => {
  await setExplained(api, true);
});

for (const [device, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
] as const) {
  test.describe(device, () => {
    test.use({ viewport });

    test("a session: start, a streamed reply, and a message that reaches the host as written", async ({
      page,
      api,
      playwright,
    }) => {
      const name = `chat-${device}-${Date.now()}`;
      await withHost(api, playwright, name, async (host) => {
        const { chat, first } = await startSession(page, host, name, device);
        await host.say(first.conversation, "It adds ");
        await expect(chat.locator(".conversation__message--assistant")).toContainText("It adds");
        await expect(chat.getByText(/^Working…/)).toBeVisible();
        await host.say(first.conversation, "a chat view.");
        await host.endTurn(first.conversation, 1);
        await expect(chat.getByText(/^Working…/)).toHaveCount(0);

        await chat.getByLabel("Message").fill("And the tests?");
        await chat.getByRole("button", { name: "Send" }).click();
        const second = await host.nextItemOfType("message");
        expect(second.text).toBe("And the tests?");
        await expect(chat.locator(".conversation__message--user").last()).toContainText("And the tests?");
        await host.say(second.conversation, "They pass.");
        await host.endTurn(second.conversation, 2);
        await expect(chat.getByText(/^Working…/)).toHaveCount(0);
      });
    });

    test("Disconnect asks first while a reply is under way, and only then", async ({ page, api, playwright }) => {
      const name = `disconnect-ask-${device}-${Date.now()}`;
      await withHost(api, playwright, name, async (host) => {
        const { chat, first } = await startSession(page, host, name, device);
        await host.say(first.conversation, "It adds ");
        await expect(chat.getByText(/^Working…/)).toBeVisible();

        await openSessionMenu(page, device);
        await expect(fact(chat, "State")).toHaveText("Connected");
        await chat.getByRole("button", { name: "Disconnect" }).click();
        await expect(chat.getByRole("alertdialog")).toContainText("Disconnect and stop the current reply?");
        await chat.getByRole("button", { name: "Keep it" }).click();
        await expect(chat.getByRole("alertdialog")).toHaveCount(0);
        await expect(chat.getByText(/^Working…/)).toBeVisible();
      });
    });

    test("Disconnect ends an idle session's process at once and keeps it listed; the next message reconnects it", async ({
      page,
      api,
      playwright,
    }) => {
      const name = `disconnect-${device}-${Date.now()}`;
      await withHost(api, playwright, name, async (host) => {
        const { machine, chat, start, first } = await startSession(page, host, name, device);
        await host.say(first.conversation, "a chat view.");
        await host.endTurn(first.conversation, 1);
        await expect(chat.getByText(/^Working…/)).toHaveCount(0);

        await openSessionMenu(page, device);
        await chat.getByRole("button", { name: "Disconnect" }).click();
        const stop = await host.nextItemOfType("stop");
        expect(stop.conversation).toBe(start.conversation);
        await expect(chat).toContainText("Disconnected");
        await expect(chat.getByRole("button", { name: "Disconnect" })).toHaveCount(0);
        if (device === "phone") await chat.getByRole("button", { name: "‹ Sessions" }).click();
        await openSessions(page);
        await expect(machine.getByRole("button", { name: /Disconnected/ })).toBeVisible();
        if (device === "phone") await machine.getByRole("button", { name: /Disconnected/ }).click();

        await chat.getByLabel("Message").fill("Are you back?");
        await chat.getByRole("button", { name: "Send" }).click();
        const restart = await host.nextItemOfType("start");
        expect(restart).toMatchObject({ conversation: start.conversation, resume: true });
        await host.ready(restart);
        const again = await host.nextItemOfType("message");
        expect(again.text).toBe("Are you back?");
        await openSessionMenu(page, device);
        await expect(fact(chat, "State")).toHaveText("Connected");
      });
    });

    test("what the remote control proposes shows in the chat, with the way to approve it", async ({
      page,
      api,
      playwright,
    }) => {
      const name = `proposal-${device}-${Date.now()}`;
      await withHost(api, playwright, name, async (host, conductor) => {
        await startSession(page, host, name, device);

        const ticket = await (await api.post("/api/tickets", { data: { title: `Chat ${name}`, status: "preparing" } })).json();
        const mcp = await McpSession.open(api, conductor.accessToken);
        const proposed = await mcp.callTool("propose_ready", { ticket_id: ticket.id });
        expect(proposed.isError, proposed.text).toBe(false);
        const proposals = page.getByRole("list", { name: "Proposed by this remote control" });
        const pending = (await (await api.get("/api/proposals?state=pending")).json()) as {
          proposals: { ticket_id: number; action_line: string }[];
        };
        const actionLine = pending.proposals.find((p) => p.ticket_id === ticket.id)!.action_line;
        await expect(proposals).toContainText(actionLine);

        const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
        expect(fits, "the page scrolls sideways").toBe(true);

        await proposals.getByRole("link", { name: "Review and approve on the Approvals tab" }).click();
        await expect(page).toHaveURL(/\/approvals/);
      });
    });

    test("Enter sends by default, and the choice to make it a newline is kept in this browser across a reload", async ({ page, api }) => {
      const name = `enter-${device}-${Date.now()}`;
      await registerConductor(api, name);
      const open = async () => {
        await page.goto("/remote-control");
        await openSessions(page);
        await page.getByRole("region", { name }).getByRole("button", { name: "+ New session" }).click();
        return { box: page.getByRole("textbox", { name: "Message" }), enterSends: page.getByRole("checkbox", { name: "Enter sends" }) };
      };

      let { box, enterSends } = await open();
      await expect(enterSends).toBeChecked();
      await expect(box).toHaveAttribute("enterkeyhint", "send");
      await box.fill("a");
      await box.press("Shift+Enter");
      await box.pressSequentially("b");
      await expect(box).toHaveValue("a\nb");

      await enterSends.uncheck();

      ({ box, enterSends } = await open());
      await expect(enterSends).not.toBeChecked();
      await expect(box).toHaveAttribute("enterkeyhint", "enter");
      await box.fill("a");
      await box.press("Enter");
      await box.pressSequentially("b");
      await expect(box).toHaveValue("a\nb");
    });

    test("a switch that would stop a reply under way asks first; Keep it leaves it replying, and confirming stops it", async ({
      page,
      api,
      playwright,
    }) => {
      const conductor = await registerConductor(api, `switch-${device}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const { stopped, live } = await twoSessions(api, host, conductor.id);
        await host.say(live, "still working");

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${stopped}`);
        const chat = page.getByRole("region", { name: "Session" });
        await openSessionMenu(page, device);
        await expect(chat).toContainText("Disconnected");
        await chat.getByLabel("Message").fill("back to the first one");
        await chat.getByRole("button", { name: "Send" }).click();

        const ask = chat.getByRole("alertdialog", { name: "Confirm switch" });
        await expect(ask).toContainText('Session "second question" is replying. Stop it and continue here?');
        await expect(ask.getByRole("checkbox")).toHaveCount(0);

        // Keep it: nothing was sent, the draft is still there, and the other
        // session is still the live one.
        await ask.getByRole("button", { name: "Keep it" }).click();
        await expect(ask).toHaveCount(0);
        await expect(chat.getByLabel("Message")).toHaveValue("back to the first one");
        const stillLive = await (await api.get(`/api/conductors/${conductor.id}/conversations/${live}`)).json();
        expect(stillLive.state).toBe("ready");

        await chat.getByRole("button", { name: "Send" }).click();
        await ask.getByRole("button", { name: "Stop it and continue" }).click();
        const stop = await host.nextItemOfType("stop");
        expect(stop.conversation).toBe(live);
        const restart = await host.nextItemOfType("start");
        expect(restart).toMatchObject({ conversation: stopped, resume: true });
        await host.ready(restart);
        expect((await host.nextItemOfType("message")).text).toBe("back to the first one");

        const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
        expect(fits, "the page scrolls sideways").toBe(true);
      } finally {
        await host.close();
      }
    });

    test("the first switch explains what carries over, once Do not show again is saved it does not", async ({ page, api, playwright }) => {
      const conductor = await registerConductor(api, `explain-${device}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const { stopped, live } = await twoSessions(api, host, conductor.id);
        await setExplained(api, false);

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${stopped}`);
        const chat = page.getByRole("region", { name: "Session" });
        await chat.getByLabel("Message").fill("one");
        await chat.getByRole("button", { name: "Send" }).click();

        const dialog = chat.getByRole("alertdialog", { name: "Switching sessions" });
        await expect(dialog).toContainText("Not carried over");
        const box = await dialog.getByRole("button", { name: "Continue" }).boundingBox();
        if (device === "phone") expect(box!.height).toBeGreaterThanOrEqual(44);
        const saved = page.waitForResponse((r) => r.url().includes(EXPLAINED) && r.request().method() === "PUT");
        await dialog.getByRole("checkbox", { name: "Do not show again" }).check();
        await dialog.getByRole("button", { name: "Continue" }).click();
        expect((await saved).status()).toBe(200);
        expect((await host.nextItemOfType("stop")).conversation).toBe(live);
        await host.ready(await host.nextItemOfType("start"));
        await host.nextItemOfType("message");
        await host.say(stopped, "done");
        await host.endTurn(stopped, 2);

        // Back to the other one: a takeover again, of an idle session, and
        // the explanation is not shown. The stop reaching the host shows the
        // send went through with nothing asked.
        await page.goto(`/remote-control?conductor=${conductor.id}&session=${live}`);
        await chat.getByLabel("Message").fill("two");
        await chat.getByRole("button", { name: "Send" }).click();
        expect((await host.nextItemOfType("stop")).conversation).toBe(stopped);
        await expect(chat.getByRole("alertdialog")).toHaveCount(0);
      } finally {
        await host.close();
        await setExplained(api, true);
      }
    });

    test("Up from the top line lists the last prompts above the box; choosing one fills it and sends nothing until Send", async ({
      page,
      api,
      playwright,
    }) => {
      const conductor = await registerConductor(api, `history-${device}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const started = await api.post(`/api/conductors/${conductor.id}/conversations`, {
          data: { message: "first prompt", client_id: crypto.randomUUID() },
        });
        expect(started.status(), await started.text()).toBe(201);
        const { id } = (await started.json()) as { id: string };
        await host.ready(await host.nextItemOfType("start"));
        await host.nextItemOfType("message");
        await host.say(id, "ok");
        await host.endTurn(id, 1);

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
        const chat = page.getByRole("region", { name: "Session" });
        const box = chat.getByRole("textbox", { name: "Message" });
        const multi = "second prompt, which is long\nand has a second line";
        await expect(chat).toContainText("first prompt");
        // Sent with Enter, which also checks the new prompt joins the list
        // without a reload.
        await box.fill(multi);
        await box.press("Enter");
        expect((await host.nextItemOfType("message")).text).toBe(multi);
        await host.say(id, "ok");
        await host.endTurn(id, 2);
        await box.fill("third prompt");
        await box.press("Enter");
        expect((await host.nextItemOfType("message")).text).toBe("third prompt");
        await host.say(id, "ok");
        await host.endTurn(id, 3);
        await expect(chat.getByText(/^Working…/)).toHaveCount(0);

        // A draft makes Send available, so a choosing Enter that also sent
        // would send it.
        await box.fill("a draft");
        await box.press("ArrowUp");
        const list = chat.getByRole("listbox");
        const options = list.getByRole("option");
        await expect(options).toHaveText(["first prompt", "second prompt, which is long…", "third prompt"]);
        await expect(options.last()).toHaveAttribute("aria-selected", "true");
        const listBox = (await list.boundingBox())!;
        const boxBox = (await box.boundingBox())!;
        const viewport = page.viewportSize()!;
        expect(listBox.y + listBox.height, "the list ends above the box").toBeLessThanOrEqual(boxBox.y);
        expect(listBox.x).toBeGreaterThanOrEqual(0);
        expect(listBox.x + listBox.width).toBeLessThanOrEqual(viewport.width);
        expect(listBox.y).toBeGreaterThanOrEqual(0);

        // Choosing fills the box and sends nothing.
        await box.press("ArrowUp");
        await box.press("Enter");
        await expect(list).toHaveCount(0);
        await expect(box).toHaveValue(multi);
        await expect(chat.locator(".conversation__message--user")).toHaveCount(3);

        await chat.getByRole("button", { name: "Send" }).click();
        expect((await host.nextItemOfType("message")).text).toBe(multi);
        await expect(chat.locator(".conversation__message--user")).toHaveCount(4);
      } finally {
        await host.close();
      }
    });

    test("the message box stays in view, with Send to its right, however long the transcript is", async ({
      page,
      api,
      playwright,
    }) => {
      const name = `composer-${device}-${Date.now()}`;
      const conductor = await registerConductor(api, name);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const started = await api.post(`/api/conductors/${conductor.id}/conversations`, {
          data: { message: "go", client_id: crypto.randomUUID() },
        });
        expect(started.status(), await started.text()).toBe(201);
        const { id } = (await started.json()) as { id: string };
        const start = await host.nextItemOfType("start");
        await host.ready(start);
        await host.nextItemOfType("message");
        for (let turn = 1; turn <= 30; turn++) {
          await host.say(id, `Line ${turn}: ${"a long reply ".repeat(8)}`);
          await host.endTurn(id, turn);
        }

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
        const chat = page.getByRole("region", { name: "Session" });
        await expect(chat).toContainText("Line 30");

        const box = chat.getByRole("textbox", { name: "Message" });
        const send = chat.getByRole("button", { name: "Send" });
        const inView = async () => {
          const b = (await box.boundingBox())!;
          const s = (await send.boundingBox())!;
          const bottom = Math.max(b.y + b.height, s.y + s.height);
          return b.y >= 0 && s.y >= 0 && bottom <= viewport.height;
        };

        // The transcript is longer than the page, and the box is in view
        // wherever it has been scrolled to. The transcript scrolls in its own
        // region, with the box below it.
        const transcript = chat.getByRole("list", { name: "Transcript" });
        const scrollable = await transcript.evaluate((el) => el.scrollHeight > el.clientHeight * 2);
        expect(scrollable, "the transcript should be longer than two screens").toBe(true);
        for (const y of [0, 600, 100000]) {
          await transcript.evaluate((el, top) => el.scrollTo(0, top), y);
          expect(await inView(), `the message box is out of view at scroll ${y}`).toBe(true);
        }

        // One row: Send is right of the box, and they overlap vertically.
        const b = (await box.boundingBox())!;
        const s = (await send.boundingBox())!;
        expect(s.x, "Send is not to the right of the box").toBeGreaterThanOrEqual(b.x + b.width);
        expect(s.y < b.y + b.height && b.y < s.y + s.height, "Send is not on the box's row").toBe(true);

        const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
        expect(fits, "the page scrolls sideways").toBe(true);
      } finally {
        await host.close();
      }
    });

    test("the history and the message box never overlap, at any scroll position or box height", async ({ page, api, playwright }) => {
      const conductor = await registerConductor(api, `overlap-${device}-${Date.now()}`);
      const host = await FakeConductorHost.open(playwright, conductor.accessToken);
      try {
        const started = await api.post(`/api/conductors/${conductor.id}/conversations`, {
          data: { message: "go", client_id: crypto.randomUUID() },
        });
        expect(started.status(), await started.text()).toBe(201);
        const { id } = (await started.json()) as { id: string };
        await host.ready(await host.nextItemOfType("start"));
        await host.nextItemOfType("message");
        for (let turn = 1; turn <= 30; turn++) {
          await host.say(id, `Line ${turn}: ${"a long reply ".repeat(8)}`);
          await host.endTurn(id, turn);
        }

        await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
        const chat = page.getByRole("region", { name: "Session" });
        await expect(chat).toContainText("Line 30");
        const transcript = chat.getByRole("list", { name: "Transcript" });
        const box = chat.getByRole("textbox", { name: "Message" });
        const composer = chat.locator(".message-composer");

        // Every message the person can see: the part of it inside both the
        // transcript's own box and the window. None may reach into the
        // composer.
        const expectNoOverlap = async (when: string) => {
          const c = (await composer.boundingBox())!;
          const t = (await transcript.boundingBox())!;
          const top = Math.max(t.y, 0);
          const bottom = Math.min(t.y + t.height, viewport.height);
          const msgs = await chat.locator(".conversation__message").evaluateAll((els) =>
            els.map((el) => {
              const r = el.getBoundingClientRect();
              return { top: r.top, bottom: r.bottom };
            }),
          );
          const visible = msgs
            .map((m) => ({ top: Math.max(m.top, top), bottom: Math.min(m.bottom, bottom) }))
            .filter((m) => m.bottom > m.top);
          expect(visible.length, `${when}: no message on screen`).toBeGreaterThan(0);
          for (const m of visible) {
            expect(
              m.bottom <= c.y || m.top >= c.y + c.height,
              `${when}: a message (${m.top}..${m.bottom}) overlaps the composer (${c.y}..${c.y + c.height})`,
            ).toBe(true);
          }
        };
        // Scrolls whatever scrolls the transcript -- its own region, or the
        // page around it if the layout ever goes back to one shared scroll --
        // so the check is about what the person sees, not about which element
        // is the scroller.
        const scrollTo = async (top: number) => {
          const reached = await transcript.evaluate((el, y) => {
            let node: HTMLElement | SVGElement | null = el;
            while (node && !(node.scrollHeight > node.clientHeight && /auto|scroll/.test(getComputedStyle(node).overflowY))) {
              node = node.parentElement;
            }
            node?.scrollTo(0, y);
            return node ? node.scrollTop : null;
          }, top);
          expect(reached, `nothing scrolls the transcript, so scroll ${top} was not tried`).not.toBeNull();
          return reached!;
        };
        const atBottom = async (when: string) => {
          expect(await scrollTo(1e6), `${when}: scrolled to the bottom`).toBeGreaterThan(1000);
          const last = (await chat.locator(".conversation__message").last().boundingBox())!;
          const c = (await composer.boundingBox())!;
          expect(last.y + last.height, `${when}: the last message ends below the composer's top`).toBeLessThanOrEqual(c.y);
        };

        for (const [when, fill] of [
          ["one line", ""],
          ["ten lines", Array.from({ length: 10 }, (_, i) => `draft ${i}`).join("\n")],
        ] as const) {
          await box.fill(fill);
          if (fill) {
            const h = (await box.boundingBox())!.height;
            expect(h, "the box grows").toBeGreaterThan(80);
          }
          await atBottom(when);
          await expectNoOverlap(`${when} at the bottom`);
          for (const y of [0, 300, 900]) {
            expect(await scrollTo(y), `${when}: scrolled to ${y}`).toBe(y);
            await expectNoOverlap(`${when} at scroll ${y}`);
          }
        }
      } finally {
        await host.close();
      }
    });
  });
}

// A fresh browser on a touch device has made no choice: Enter is a newline,
// whatever the account says, and opting in works there too.
test("on a touch device Enter starts off as a newline, and the person can turn it on", async ({ browser, api, frontendApi }) => {
  const name = `enter-touch-${Date.now()}`;
  await registerConductor(api, name);
  const context = await browser.newContext({ viewport: PHONE, hasTouch: true, isMobile: true });
  try {
    await rejectAllConsent(context);
    await context.addCookies((await frontendApi.storageState()).cookies);
    const page = await context.newPage();
    await page.goto("/remote-control");
    await openSessions(page);
    await page.getByRole("region", { name }).getByRole("button", { name: "+ New session" }).click();
    const box = page.getByRole("textbox", { name: "Message" });
    const enterSends = page.getByRole("checkbox", { name: "Enter sends" });

    await expect(enterSends).not.toBeChecked();
    await expect(box).toHaveAttribute("enterkeyhint", "enter");
    await box.fill("a");
    await box.press("Enter");
    await box.pressSequentially("b");
    await expect(box).toHaveValue("a\nb");

    await enterSends.check();
    await expect(box).toHaveAttribute("enterkeyhint", "send");
  } finally {
    await context.close();
  }
});

test("starting another session takes over from the live one, with no stop-it-first refusal", async ({ page, api, playwright }) => {
  const name = `takeover-${Date.now()}`;
  const conductor = await registerConductor(api, name);
  const host = await FakeConductorHost.open(playwright, conductor.accessToken);
  try {
    const startSession = async (message: string) => {
      const resp = await api.post(`/api/conductors/${conductor.id}/conversations`, {
        data: { message, client_id: crypto.randomUUID() },
      });
      expect(resp.status(), await resp.text()).toBe(201);
      return (await resp.json()) as { id: string };
    };

    const a = await startSession("first");
    const startA = await host.nextItemOfType("start");
    expect(startA.conversation).toBe(a.id);
    await host.ready(startA);
    await host.nextItemOfType("message");

    const b = await startSession("second");
    const stopA = await host.nextItemOfType("stop");
    expect(stopA.conversation).toBe(a.id);
    const startB = await host.nextItemOfType("start");
    expect(startB.conversation).toBe(b.id);

    await page.goto(`/remote-control?conductor=${conductor.id}&session=${a.id}`);
    await expect(page.getByRole("region", { name: "Session" })).toContainText("Disconnected");
    await expect(page.getByRole("region", { name: "Session" })).toContainText("Replaced by another session");
  } finally {
    await host.close();
  }
});
