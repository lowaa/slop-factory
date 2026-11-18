import { DEV_USER_EMAIL, DEV_USER_PASSWORD } from "@/support/env";
import { expect, signedInApiContext, test } from "./support/fixtures";
import { credentialSupervisorViaCodeFlow } from "./support/oauth";
import { setProvisionFromBoard, StubSupervisor } from "./support/stub_supervisor";
import { chooseFromDropdown } from "./support/widgets";

// The board's side of provisioning from end to end, with the spec standing in
// for the supervisor: the real supervisor's reconciler is covered by its own
// tests (supervisor/), and what is checked here is the contract between them,
// the document the board writes and the reports it reads back.

test("an owner adds, pauses, resumes and retires a slot from the board", async ({ page, api }) => {
  const machine = `box-${Date.now()}`;
  const { accessToken } = await credentialSupervisorViaCodeFlow(api, machine);
  const supervisor = new StubSupervisor(api, { Authorization: `Bearer ${accessToken}` });

  // Off, the supervisor reads an empty document and the board shows no machine group.
  await page.goto("/workers");
  await expect(page.getByRole("region", { name: `Workers on ${machine}` })).toHaveCount(0);
  expect((await supervisor.document()).agents).toEqual([]);

  await setProvisionFromBoard(api, true);
  await page.reload();
  const group = page.getByRole("region", { name: `Workers on ${machine}` });
  // The stub supervisor never makes a worker of the entry, so it stays on the group's waiting list.
  const list = group.getByRole("list", { name: `Waiting on ${machine}` });

  await group.getByLabel("Name", { exact: true }).fill("alpha");
  await group.getByRole("button", { name: "Add worker" }).click();
  const row = list.getByRole("listitem").filter({ hasText: "alpha" });
  await expect(row).toContainText("Waiting for supervisor");

  expect((await supervisor.agent("alpha")).kind).toBe("slot");
  await supervisor.report("alpha", "provisioning");
  await page.reload();
  await expect(row).toContainText("Provisioning");
  await supervisor.report("alpha", "running");
  await page.reload();
  await expect(row).toContainText("Running");

  await row.getByRole("button", { name: "Pause alpha" }).click();
  await expect(row).toContainText("Waiting for supervisor");
  await supervisor.expectState("alpha", "paused");
  await supervisor.report("alpha", "paused", "held by the owner");
  await page.reload();
  await expect(row).toContainText("Paused by you");

  await row.getByRole("button", { name: "Resume alpha" }).click();
  await supervisor.expectState("alpha", "running");
  await supervisor.report("alpha", "running");
  await page.reload();
  await expect(row).toContainText("Running");

  // A slot with unpushed work is refused, and the board says why.
  await row.getByRole("button", { name: "Retire alpha" }).click();
  await expect(row).toContainText("Retiring");
  await supervisor.report("alpha", "failed", "unpushed work in the slot's clones");
  await page.reload();
  await expect(row).toContainText("Failed: unpushed work in the slot's clones");

  // Once the work is pushed the supervisor drains, revokes, deletes and reports it retired.
  await supervisor.report("alpha", "retired");
  await page.reload();
  await expect(row).toHaveCount(0);
});

test("an owner adds a Remote Control identity and sees the command to run", async ({ page, api }) => {
  const machine = `rc-box-${Date.now()}`;
  await credentialSupervisorViaCodeFlow(api, machine);
  await setProvisionFromBoard(api, true);

  await page.goto("/workers");
  const group = page.getByRole("region", { name: `Workers on ${machine}` });
  await chooseFromDropdown(page, "Kind", "Remote Control");
  await chooseFromDropdown(page, "Launch", "Run by hand");
  await group.getByLabel("Name", { exact: true }).fill("phone");
  await group.getByRole("button", { name: "Add worker" }).click();

  const row = group.getByRole("list", { name: `Waiting on ${machine}` }).getByRole("listitem").filter({ hasText: "phone" });
  await expect(row).toContainText("Remote Control");
  await expect(row).toContainText(".slop-haus/run_remote_control.sh");
});

// `setup` and `login` on a host are the supervisor's side (covered in supervisor/); what the board sees of a
// host that has just run them is a registered supervisor, which credentialSupervisorViaCodeFlow stands in for.
test("a new tenant provisions from the board without the owner switching it on", async ({ playwright }) => {
  const owner = await signedInApiContext(playwright, DEV_USER_EMAIL, DEV_USER_PASSWORD);
  try {
    const created = await owner.post("/api/tenants", { data: { name: `Fresh ${Date.now()}` } });
    expect(created.status(), await created.text()).toBeLessThan(300);

    const setting = await owner.get("/api/settings/provision_from_board");
    expect((await setting.json()).bool_val, "the tenant started with it on").toBe(true);

    const machine = `fresh-box-${Date.now()}`;
    const { accessToken, agentId } = await credentialSupervisorViaCodeFlow(owner, machine);
    const supervisor = new StubSupervisor(owner, { Authorization: `Bearer ${accessToken}` });
    const empty = await supervisor.document();
    expect(empty.agents).toEqual([]);

    // The board's "Add worker" is this write (the add-a-machine flow itself is the first test above).
    const added = await owner.put(`/api/supervisors/${agentId}/desired_state`, {
      data: { revision: empty.revision, agents: [{ kind: "slot", name: "first", state: "running", resume_generation: 0 }] },
    });
    expect(added.status(), await added.text()).toBe(200);
    expect((await supervisor.agent("first")).kind).toBe("slot");
  } finally {
    await owner.dispose();
  }
});
