import type { APIRequestContext, Locator, Page } from "@playwright/test";

import type { FakeConductorHost } from "./fake_conductor_host";
import { expect } from "./fixtures";

export const DESKTOP = { width: 1280, height: 900 };
export const PHONE = { width: 390, height: 844 };

export type Device = "desktop" | "phone";

// The sessions panel starts closed on a phone and open on a desktop.
export async function openSessions(page: Page): Promise<void> {
  const show = page.getByRole("button", { name: "Show sessions" });
  if (await show.isVisible()) await show.click();
}

// On a phone the chat is a screen of its own: the session's facts and
// Disconnect are in the bar's menu, which a press elsewhere closes again.
export async function openSessionMenu(page: Page, device: Device): Promise<void> {
  if (device !== "phone") return;
  const button = page.getByRole("button", { name: "Session menu" });
  if ((await button.getAttribute("aria-expanded")) !== "true") await button.click();
}

// One of the session's facts (Machine, Host, State) by its label. The
// transcript has its own "Connected" note, which arrives ahead of the facts
// being read again, so the chat's text cannot say what the session's state is.
export function fact(chat: Locator, label: string): Locator {
  return chat.getByRole("term").filter({ hasText: new RegExp(`^${label}$`) }).locator("xpath=following-sibling::dd[1]");
}

export const EXPLAINED = "/api/me/settings/remote_control.session_switch_explained";

export async function setExplained(api: APIRequestContext, value: boolean): Promise<void> {
  const resp = await api.put(EXPLAINED, { data: { type: "bool", bool_val: value } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
}

// Two sessions on one machine through the API: the first is left with no
// process (the second replaced it), the second is the live one. The host has
// taken everything it was handed, so what it is handed next is what the page
// causes.
export async function twoSessions(api: APIRequestContext, host: FakeConductorHost, conductorId: string) {
  const start = async (message: string) => {
    const resp = await api.post(`/api/conductors/${conductorId}/conversations`, {
      data: { message, client_id: crypto.randomUUID() },
    });
    expect(resp.status(), await resp.text()).toBe(201);
    return (await resp.json()) as { id: string };
  };
  const first = await start("first question");
  await host.ready(await host.nextItemOfType("start"));
  await host.nextItemOfType("message");
  await host.say(first.id, "first answer");
  await host.endTurn(first.id, 1);

  const second = await start("second question");
  expect((await host.nextItemOfType("stop")).conversation).toBe(first.id);
  await host.ready(await host.nextItemOfType("start"));
  await host.nextItemOfType("message");
  return { stopped: first.id, live: second.id };
}
