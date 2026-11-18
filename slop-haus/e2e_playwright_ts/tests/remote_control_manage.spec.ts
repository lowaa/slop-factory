// Managing a remote control from the sessions sidebar: Remove and Restore an
// agent, and Delete a session. Against a fake host
// (support/fake_conductor_host.ts) so a session exists without a model.

import { FakeConductorHost } from "./support/fake_conductor_host";
import { expect, test } from "./support/fixtures";
import { registerConductor } from "./support/api_helpers";

test.beforeEach(async ({ api }) => {
  const resp = await api.put("/api/me/settings/remote_control.session_switch_explained", { data: { type: "bool", bool_val: true } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
});

test("remove a remote control from the sidebar, then restore it from Show removed", async ({ page, api }) => {
  const name = `manage-${Date.now()}`;
  await registerConductor(api, name);

  await page.goto("/remote-control");
  const machine = page.getByRole("region", { name });
  await expect(machine).toBeVisible();

  await machine.getByRole("button", { name: `Manage ${name}` }).click();
  await machine.getByRole("button", { name: "Remove" }).click();
  const confirm = machine.getByRole("group", { name: `Confirm removing ${name}` });
  await expect(confirm).toContainText("you can restore it");
  await confirm.getByRole("button", { name: "Remove" }).click();

  // Gone from the sidebar, with its sessions.
  await expect(machine).toHaveCount(0);

  await page.getByRole("checkbox", { name: /Show removed/ }).check();
  const removed = page.getByRole("list", { name: "Removed remote controls" }).locator("li", { hasText: name });
  await expect(removed).toBeVisible();
  await removed.getByRole("button", { name: "Restore" }).click();

  await expect(page.getByRole("region", { name })).toBeVisible();
});

test("delete a session: its transcript is gone and it leaves the sidebar", async ({ page, api, playwright }) => {
  const name = `delete-${Date.now()}`;
  const conductor = await registerConductor(api, name);
  const host = await FakeConductorHost.open(playwright, conductor.accessToken);
  try {
    const started = await api.post(`/api/conductors/${conductor.id}/conversations`, {
      data: { message: "a question to forget", client_id: crypto.randomUUID() },
    });
    expect(started.status(), await started.text()).toBe(201);
    const { id } = (await started.json()) as { id: string };
    await host.ready(await host.nextItemOfType("start"));
    await host.nextItemOfType("message");
    await host.say(id, "an answer to forget");
    await host.endTurn(id, 1);
    // Delete is refused while the host has work it has not acknowledged.
    await host.acknowledge();

    await page.goto(`/remote-control?conductor=${conductor.id}&session=${id}`);
    const chat = page.getByRole("region", { name: "Session", exact: true });
    await expect(chat).toContainText("an answer to forget");

    const confirmDelete = async () => {
      await chat.getByRole("button", { name: "Delete", exact: true }).click();
      const dialog = chat.getByRole("alertdialog", { name: "Confirm delete" });
      await expect(dialog).toContainText("transcript is gone for good");
      await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    };

    // A live session is ended first; its stop is work the host has yet to
    // acknowledge, so the first delete is refused and nothing is lost.
    await confirmDelete();
    await expect(chat.getByRole("alert")).toBeVisible();
    await host.nextItemOfType("stop");
    await host.acknowledge();
    await confirmDelete();

    await expect(page.getByRole("region", { name })).toBeVisible();
    // Back on the machine, not on the deleted session.
    await expect(page).not.toHaveURL(/session=/);
    await expect(page.getByText("an answer to forget")).toHaveCount(0);
    const gone = await api.get(`/api/conductors/${conductor.id}/conversations/${id}`);
    expect(gone.status()).toBe(404);
  } finally {
    await host.close();
  }
});
