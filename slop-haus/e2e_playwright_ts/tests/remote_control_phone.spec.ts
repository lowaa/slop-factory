// Remote Control on a phone is two screens, the list of sessions and the chat,
// told apart by the address. Against a fake host (support/fake_conductor_host.ts)
// this shows the path between them: open a session from the list, talk, and
// Back lands on the list without the setup panels having been in the way.

import { FakeConductorHost } from "./support/fake_conductor_host";
import { expect, test } from "./support/fixtures";
import { registerConductor } from "./support/api_helpers";

const PHONE = { width: 390, height: 844 };

test.use({ viewport: PHONE });

test.beforeEach(async ({ api }) => {
  const resp = await api.put("/api/me/settings/remote_control.session_switch_explained", { data: { type: "bool", bool_val: true } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
});

test("open a session from the list, send a message, and Back returns to the list", async ({ page, api, playwright }) => {
  const name = `phone-${Date.now()}`;
  const conductor = await registerConductor(api, name);
  const host = await FakeConductorHost.open(playwright, conductor.accessToken);
  try {
    const started = await api.post(`/api/conductors/${conductor.id}/conversations`, {
      data: { message: "first question", client_id: crypto.randomUUID() },
    });
    expect(started.status(), await started.text()).toBe(201);
    const { id } = (await started.json()) as { id: string };
    await host.ready(await host.nextItemOfType("start"));
    await host.nextItemOfType("message");
    for (let turn = 1; turn <= 20; turn++) {
      await host.say(id, `Line ${turn}: ${"a long reply ".repeat(8)}`);
      await host.endTurn(id, turn);
    }

    const setup = page.getByText("Add a remote control", { exact: true });
    const help = page.getByText("Running a remote control", { exact: true });
    const sessions = page.getByRole("region", { name });

    // The list screen: the machine and its sessions, then the setup and help.
    await page.goto(`/remote-control?conductor=${conductor.id}&new=1`);
    await expect(sessions).toBeVisible();
    await expect(page.getByRole("button", { name: "Show sessions" })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Session", exact: true })).toHaveCount(0);
    await expect(setup).toBeVisible();
    await expect(help).toBeVisible();

    await sessions.getByRole("button", { name: /Connected/ }).click();
    await expect(page).toHaveURL(/session=/);

    // The chat screen: only the chat, with the transcript taking most of the
    // height and the message box in view, and the page itself not scrolling.
    const chat = page.getByRole("region", { name: "Session", exact: true });
    await expect(chat).toContainText("Line 20");
    await expect(setup).toHaveCount(0);
    await expect(help).toHaveCount(0);
    await expect(sessions).toHaveCount(0);
    const transcript = (await chat.getByRole("list", { name: "Transcript" }).boundingBox())!;
    expect(transcript.height, "the transcript has most of the height").toBeGreaterThan(PHONE.height * 0.5);
    const box = (await chat.getByRole("textbox", { name: "Message" }).boundingBox())!;
    expect(box.y + box.height, "the message box is in view").toBeLessThanOrEqual(PHONE.height);
    const pageScrolls = await page.evaluate(() => document.documentElement.scrollHeight > document.documentElement.clientHeight);
    expect(pageScrolls, "the page scrolls").toBe(false);

    await chat.getByLabel("Message").fill("And the tests?");
    await chat.getByRole("button", { name: "Send" }).click();
    expect((await host.nextItemOfType("message")).text).toBe("And the tests?");
    await expect(chat.locator(".conversation__message--user").last()).toContainText("And the tests?");

    // Back is the address changing: the list again, and not the session.
    await chat.getByRole("button", { name: "‹ Sessions" }).click();
    await expect(page).not.toHaveURL(/session=/);
    await expect(sessions).toBeVisible();
    await expect(page.getByRole("region", { name: "Session", exact: true })).toHaveCount(0);
    await expect(setup).toBeVisible();

    // And the browser's own Back goes the other way, to the chat.
    await page.goBack();
    await expect(page).toHaveURL(/session=/);
    await expect(page.getByRole("region", { name: "Session", exact: true })).toContainText("And the tests?");
  } finally {
    await host.close();
  }
});
