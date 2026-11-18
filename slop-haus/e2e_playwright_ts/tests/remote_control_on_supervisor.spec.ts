// Adding a remote control from the Remote Control page on a machine whose
// supervisor the person signed in, with the spec standing in for that
// supervisor (support/stub_supervisor.ts): the real supervisor and host are
// harness_e2e/scenarios/test_supervised_remote_control.py's. What is checked
// here is the page's side: the entry it writes, the progress it shows, the
// machine it lists once the supervisor has made the conductor, and Remove.

import { DEV_USER_PASSWORD } from "@/support/env";
import { expect, test } from "./support/fixtures";
import { credentialSupervisorViaCodeFlow } from "./support/oauth";
import { provisionFromBoard, setProvisionFromBoard, StubSupervisor } from "./support/stub_supervisor";

test.beforeEach(async ({ api }) => {
  const resp = await api.put("/api/me/settings/remote_control.session_switch_explained", { data: { type: "bool", bool_val: true } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
});

test("add a remote control on your supervisor, watch it come up, then remove it", async ({ page, api }) => {
  const machine = `rc-box-${Date.now()}`;
  const name = `on-box-${Date.now()}`;
  const { accessToken } = await credentialSupervisorViaCodeFlow(api, machine);
  const supervisor = new StubSupervisor(api, { Authorization: `Bearer ${accessToken}` });
  // Other specs on this stack read the switch as they found it.
  const wasOn = await provisionFromBoard(api);
  await setProvisionFromBoard(api, true);
  try {
    await page.goto("/remote-control?view=setup");
    const form = page.getByRole("form", { name: "Add a remote control on a machine" });
    await expect(form).toBeVisible();
    // The person may have more than one supervisor of their own by now: pick this spec's.
    const picker = form.getByLabel("Machine");
    if (await picker.count()) {
      await picker.click();
      await page.getByRole("option", { name: machine }).click();
    }
    await form.getByLabel("Remote control name").fill(name);
    await form.getByRole("button", { name: "Add remote control" }).click();
    // Adding one needs a recent sign-in, which this session may have outlived.
    const stepUp = page.getByText("Confirm it’s you");
    const progress = page.getByRole("list", { name: "Remote controls your supervisors run" }).locator("li", { hasText: name });
    await expect(stepUp.or(progress)).toBeVisible();
    if (await stepUp.isVisible()) {
      await page.getByLabel("Current password").fill(DEV_USER_PASSWORD);
      await page.getByRole("button", { name: "Confirm" }).click();
    }

    await expect(progress).toContainText(`${name} on ${machine}: Waiting for supervisor`);
    const entry = await supervisor.agent(name);
    expect(entry.kind).toBe("remote_control");
    // The page reads its supervisors again on its own, every 10 seconds: no reload.
    await supervisor.report(name, "failed", "fatal: could not read Username");
    await expect(progress).toContainText("Failed: fatal: could not read Username", { timeout: 20_000 });

    // The supervisor's enrolment makes the conductor, as the person's own.
    await supervisor.enrolRemoteControl(name);
    await supervisor.report(name, "running");
    await page.goto("/remote-control");
    const listed = page.getByRole("region", { name });
    await expect(listed).toBeVisible();

    await listed.getByRole("button", { name: `Manage ${name}` }).click();
    await expect(listed.getByRole("button", { name: "New setup script" })).toHaveCount(0);
    await listed.getByRole("button", { name: "Remove" }).click();
    await listed.getByRole("group", { name: `Confirm removing ${name}` }).getByRole("button", { name: "Remove" }).click();

    await expect(listed).toHaveCount(0);
    await supervisor.expectState(name, "removed");
  } finally {
    await setProvisionFromBoard(api, wasOn);
  }
});
