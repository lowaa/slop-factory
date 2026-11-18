import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeProfile, makeServerSettingChange, makeSetting, makeTenant, makeTenantUserSetting } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { changedByLabel } from "./setting_format";
import { ProcessingToggle } from "@/components/processing_toggle";
import TenantSettingsPage from "./tenant_settings_page";

const SELF_ID = "33333333-3333-3333-3333-333333333333";

function stub({
  settings = [makeSetting()],
  mine = [makeTenantUserSetting()],
  changes = [makeServerSettingChange()],
  tenant = makeTenant({ is_owner: true }),
}: {
  settings?: ReturnType<typeof makeSetting>[];
  mine?: ReturnType<typeof makeTenantUserSetting>[];
  changes?: ReturnType<typeof makeServerSettingChange>[];
  tenant?: ReturnType<typeof makeTenant>;
} = {}) {
  vi.spyOn(apiClient, "listSettings").mockResolvedValue(settings);
  vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue(mine);
  vi.spyOn(apiClient, "getSettingHistory").mockResolvedValue({ changes });
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([tenant]);
}

async function renderPage() {
  await renderSignedIn(<TenantSettingsPage />, { profile: makeProfile({ id: SELF_ID }) });
}

function section(name: string) {
  return screen.findByRole("region", { name });
}

describe("TenantSettingsPage", () => {
  it("lists this tenant's settings with their value and whether it is the default, asking for no history until it is opened", async () => {
    stub({ settings: [makeSetting({ bool_val: false, is_default: false })] });
    await renderPage();

    const tenant = within(await section("This tenant"));
    expect(await tenant.findByRole("switch", { name: "Ticket processing" })).not.toBeChecked();
    expect(tenant.getByText(/changed from its default/i)).toBeInTheDocument();
    expect(tenant.getByText(/any member can change the settings listed here/i)).toBeInTheDocument();
    expect(apiClient.getSettingHistory).not.toHaveBeenCalled();
  });

  it("edits the dead-holder timeout in minutes, within its bounds", async () => {
    const dead = makeSetting({
      name: "integration_lock.dead_holder_after",
      type: "int",
      bool_val: null,
      int_val: 60,
    });
    stub({ settings: [dead] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue({ ...dead, int_val: 90, is_default: false });
    const user = userEvent.setup();
    await renderPage();

    const tenant = within(await section("This tenant"));
    const field = await tenant.findByRole("spinbutton", { name: "Flag a silent integration-lock holder after" });
    expect(field).toHaveValue(60);

    await user.clear(field);
    await user.type(field, "2");
    expect(tenant.getByRole("button", { name: "Save" })).toBeDisabled();

    await user.clear(field);
    await user.type(field, "90");
    await user.click(tenant.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledWith("integration_lock.dead_holder_after", { type: "int", int_val: 90 });
  });

  it("marks a setting nobody has changed as the default, without asking for its history", async () => {
    stub();
    await renderPage();

    const tenant = within(await section("This tenant"));
    expect(await tenant.findByText("Default value")).toBeInTheDocument();
    expect(apiClient.getSettingHistory).not.toHaveBeenCalled();
  });

  it("groups and labels your settings, and shows an unlabelled one by its name", async () => {
    stub({
      mine: [
        makeTenantUserSetting({ name: "approvals.auto.status" }),
        makeTenantUserSetting({ name: "approvals.auto.edit", bool_val: true, is_default: false }),
        makeTenantUserSetting({ name: "future.setting" }),
      ],
    });
    await renderPage();

    const group = within(await screen.findByRole("group", { name: "Auto-approve your sessions' proposals" }));
    expect(group.getByRole("switch", { name: "Parking or cancelling a ticket" })).not.toBeChecked();
    expect(group.getByRole("switch", { name: "Editing a ticket's title, text, paths or priority" })).toBeChecked();
    expect(group.getByText(/other members' sessions, and agents with no owner, are never auto-approved/i)).toBeInTheDocument();
    const other = within(screen.getByRole("group", { name: "Other" }));
    expect(other.getByRole("switch", { name: "future.setting" })).toBeInTheDocument();
  });

  it("writes a tenant setting and shows the stored value", async () => {
    const user = userEvent.setup();
    stub();
    const update = vi
      .spyOn(apiClient, "updateSetting")
      .mockResolvedValue(makeSetting({ bool_val: false, is_default: false }));
    await renderPage();

    const tenant = within(await section("This tenant"));
    await user.click(await tenant.findByRole("switch", { name: "Ticket processing" }));

    expect(update).toHaveBeenCalledWith("ticket_processing_enabled", { type: "bool", bool_val: false });
    await vi.waitFor(() => expect(tenant.getByRole("switch", { name: "Ticket processing" })).not.toBeChecked());
  });

  it("writes your setting in this tenant, not the global scope", async () => {
    const user = userEvent.setup();
    stub();
    const tenantWrite = vi
      .spyOn(apiClient, "setMyTenantBoolSetting")
      .mockResolvedValue(makeTenantUserSetting({ bool_val: true, is_default: false }));
    const globalWrite = vi.spyOn(apiClient, "setMyBoolSetting");
    await renderPage();

    await user.click(await screen.findByRole("switch", { name: "Parking or cancelling a ticket" }));

    expect(tenantWrite).toHaveBeenCalledWith("approvals.auto.status", true);
    expect(globalWrite).not.toHaveBeenCalled();
    await vi.waitFor(() =>
      expect(screen.getByRole("switch", { name: "Parking or cancelling a ticket" })).toBeChecked(),
    );
  });

  it("shows an error and keeps the old value when a write fails", async () => {
    const user = userEvent.setup();
    stub();
    vi.spyOn(apiClient, "setMyTenantBoolSetting").mockRejectedValue(new ApiError(500, "boom"));
    await renderPage();

    const toggle = await screen.findByRole("switch", { name: "Parking or cancelling a ticket" });
    await user.click(toggle);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(toggle).not.toBeChecked();
    expect(toggle).toBeEnabled();
  });

  it("opens a setting's history with who, when and from to", async () => {
    const user = userEvent.setup();
    stub({
      settings: [makeSetting({ bool_val: false, is_default: false })],
      changes: [
        makeServerSettingChange({ id: 2, changed_by_id: SELF_ID, changed_by_name: "Ada" }),
        makeServerSettingChange({
          id: 1,
          changed_by_id: null,
          changed_by_name: null,
          before_history_kept: true,
          from: makeServerSettingChange().to,
          to: makeServerSettingChange().from,
        }),
      ],
    });
    await renderPage();

    await user.click(await screen.findByRole("button", { name: "History of Ticket processing" }));

    const changes = within(await screen.findByRole("list", { name: "Changes" })).getAllByRole("listitem");
    expect(changes).toHaveLength(2);
    expect(changes[0]).toHaveTextContent("you");
    expect(changes[0]).toHaveTextContent("on → off");
    expect(changes[1]).toHaveTextContent("before history was kept");
    expect(changes[1]).toHaveTextContent("off → on");
  });

  it("says when a section fails to load, without hiding the other", async () => {
    stub();
    vi.spyOn(apiClient, "listSettings").mockRejectedValue(new ApiError(500, "tenant boom"));
    await renderPage();

    await vi.waitFor(() => expect(screen.getByText("tenant boom")).toBeInTheDocument());
    expect(await screen.findByRole("switch", { name: "Parking or cancelling a ticket" })).toBeInTheDocument();
  });
});

describe("TenantSettingsPage edge cases", () => {
  it("shows a setting that is not a switch by its value, and an int, float or string as such", async () => {
    stub({
      settings: [
        makeSetting({ name: "a.count", type: "int", int_val: 7, bool_val: null }),
        makeSetting({ name: "a.ratio", type: "float", float_val: 0.5, bool_val: null }),
        makeSetting({ name: "a.word", type: "string", string_val: "hi", bool_val: null }),
      ],
    });
    await renderPage();

    const tenant = within(await section("This tenant"));
    expect(await tenant.findByText("a.count")).toBeInTheDocument();
    expect(tenant.getByText("7")).toBeInTheDocument();
    expect(tenant.getByText("0.5")).toBeInTheDocument();
    expect(tenant.getByText('"hi"')).toBeInTheDocument();
    expect(tenant.queryByRole("switch")).toBeNull();
  });

  it("shows an error and keeps the old value when a tenant-wide write fails", async () => {
    const user = userEvent.setup();
    stub();
    vi.spyOn(apiClient, "updateSetting").mockRejectedValue(new ApiError(403, "not allowed"));
    await renderPage();

    const tenant = within(await section("This tenant"));
    const toggle = await tenant.findByRole("switch", { name: "Ticket processing" });
    await user.click(toggle);

    expect(await tenant.findByRole("alert")).toHaveTextContent("not allowed");
    expect(toggle).toBeChecked();
  });

  it("pages through older changes with the cursor, and reports a failed page", async () => {
    const user = userEvent.setup();
    stub({ settings: [makeSetting({ bool_val: false, is_default: false })] });
    const history = vi
      .spyOn(apiClient, "getSettingHistory")
      .mockResolvedValueOnce({ changes: [makeServerSettingChange({ id: 3 })], next_before: 3 })
      .mockRejectedValueOnce(new ApiError(500, "page boom"))
      .mockResolvedValueOnce({ changes: [makeServerSettingChange({ id: 2, changed_by_name: "Older Person" })] });
    await renderPage();

    await user.click(await screen.findByRole("button", { name: "History of Ticket processing" }));
    await user.click(await screen.findByRole("button", { name: "Show older changes" }));
    expect(await screen.findByText("page boom")).toBeInTheDocument();
    expect(history).toHaveBeenLastCalledWith("ticket_processing_enabled", { limit: 20, before: 3 });

    await user.click(screen.getByRole("button", { name: "Show older changes" }));
    expect(await screen.findByText("Older Person")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Changes" })).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Show older changes" })).toBeNull();
  });

  it("says when the history fails to load", async () => {
    const user = userEvent.setup();
    stub({ settings: [makeSetting({ bool_val: false, is_default: false })] });
    vi.spyOn(apiClient, "getSettingHistory").mockRejectedValue(new ApiError(500, "history boom"));
    await renderPage();

    await user.click(await screen.findByRole("button", { name: "History of Ticket processing" }));
    expect(await screen.findByText("history boom")).toBeInTheDocument();
  });
});

describe("TenantSettingsPage with the header's processing switch", () => {
  it("follows the header's switch, and the header follows the page", async () => {
    const user = userEvent.setup();
    stub();
    let stored = makeSetting();
    vi.spyOn(apiClient, "getSetting").mockImplementation(async () => stored);
    vi.spyOn(apiClient, "listSettings").mockImplementation(async () => [stored]);
    vi.spyOn(apiClient, "updateSetting").mockImplementation(async (_name, input) => {
      stored = makeSetting({ bool_val: input.type === "bool" ? input.bool_val : true, is_default: false });
      return stored;
    });
    await renderSignedIn(
      <>
        <div data-testid="header">
          <ProcessingToggle />
        </div>
        <TenantSettingsPage />
      </>,
    );
    const header = within(screen.getByTestId("header"));
    const page = within(await section("This tenant"));

    await user.click(await header.findByRole("switch", { name: "Ticket processing" }));
    await vi.waitFor(() => expect(page.getByRole("switch", { name: "Ticket processing" })).not.toBeChecked());

    await user.click(page.getByRole("switch", { name: "Ticket processing" }));
    await vi.waitFor(() => expect(header.getByRole("switch", { name: "Ticket processing" })).toBeChecked());
  });
});

describe("TenantSettingsPage across a remount", () => {
  it("reads the settings afresh when the page is opened again", async () => {
    stub();
    const user = userEvent.setup();
    function Host() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button onClick={() => setOpen((o) => !o)}>toggle page</button>
          {open && <TenantSettingsPage />}
        </>
      );
    }
    await renderSignedIn(<Host />);
    await screen.findByRole("switch", { name: "Ticket processing" });
    await user.click(screen.getByRole("button", { name: "toggle page" }));
    expect(screen.queryByRole("switch", { name: "Ticket processing" })).toBeNull();

    // The header changed it while the page was away.
    vi.spyOn(apiClient, "listSettings").mockResolvedValue([makeSetting({ bool_val: false, is_default: false })]);
    await user.click(screen.getByRole("button", { name: "toggle page" }));

    await vi.waitFor(() => expect(screen.getByRole("switch", { name: "Ticket processing" })).not.toBeChecked());
  });
});

describe("changedByLabel", () => {
  it("keeps a removed account, a former member and the unattributed apart", () => {
    const base = makeServerSettingChange();
    expect(changedByLabel({ ...base, changed_by_deleted: true, changed_by_id: null, changed_by_name: null }, SELF_ID)).toBe("a removed account");
    expect(changedByLabel({ ...base, changed_by_former_member: true }, SELF_ID)).toBe("Bea Example (former member)");
    expect(changedByLabel({ ...base, changed_by_name: null }, SELF_ID)).toBe("a member");
  });

  describe("manage agents from the board", () => {
    const provision = (on: boolean) => makeSetting({ name: "provision_from_board", bool_val: on, is_default: false });
    const ROW = { name: "Manage agents from the board" };

    it("shows the label and description, not the key, with a link to the Workers page", async () => {
      stub({ settings: [provision(false)] });
      await renderPage();
      const tenant = within(await section("This tenant"));
      const toggle = await tenant.findByRole("switch", ROW);
      expect(toggle).toHaveAccessibleDescription(/Add, pause and remove each machine's agents/);
      expect(tenant.queryByText("provision_from_board")).toBeNull();
      expect(tenant.getByRole("link", { name: "Workers page" })).toHaveAttribute("href", "/workers");
    });

    it("asks before turning it on, naming adoption, and writes only once confirmed", async () => {
      const user = userEvent.setup();
      stub({ settings: [provision(false)] });
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(provision(true));
      await renderPage();
      const tenant = within(await section("This tenant"));
      await user.click(await tenant.findByRole("switch", ROW));

      const dialog = await tenant.findByRole("alertdialog");
      expect(dialog).toHaveTextContent("existing agents will be adopted as they are");
      expect(update).not.toHaveBeenCalled();
      await user.click(within(dialog).getByRole("button", { name: "Turn on" }));
      expect(update).toHaveBeenCalledWith("provision_from_board", { type: "bool", bool_val: true });
    });

    it("lists the consequences before turning it off, and cancelling changes nothing", async () => {
      const user = userEvent.setup();
      stub({ settings: [provision(true)] });
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(provision(false));
      await renderPage();
      const tenant = within(await section("This tenant"));
      await user.click(await tenant.findByRole("switch", ROW));

      const dialog = await tenant.findByRole("alertdialog");
      expect(within(dialog).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
        "Nothing stops: every agent keeps running.",
        "The board stops managing your machines' agents.",
        "You'll use slop-haus slot … on each machine instead.",
      ]);
      await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

      expect(update).not.toHaveBeenCalled();
      expect(tenant.queryByRole("alertdialog")).toBeNull();
      expect(tenant.getByRole("switch", ROW)).toBeChecked();
    });

    it("writes off once the owner confirms turning it off", async () => {
      const user = userEvent.setup();
      stub({ settings: [provision(true)] });
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(provision(false));
      await renderPage();
      const tenant = within(await section("This tenant"));
      await user.click(await tenant.findByRole("switch", ROW));
      await user.click(within(await tenant.findByRole("alertdialog")).getByRole("button", { name: "Turn off" }));
      expect(update).toHaveBeenCalledWith("provision_from_board", { type: "bool", bool_val: false });
    });

    it("tells a member it is the owner's and does not let them use it", async () => {
      stub({ settings: [provision(false)], tenant: makeTenant({ is_owner: false }) });
      await renderPage();
      const tenant = within(await section("This tenant"));
      expect(await tenant.findByRole("switch", ROW)).toBeDisabled();
      expect(tenant.getByText("Only an owner can change this.")).toBeInTheDocument();
    });
  });

  describe("the tenant's name", () => {
    it("lets an owner rename it and shows the new name", async () => {
      stub({ tenant: makeTenant({ name: "Ada's workspace", is_owner: true }) });
      vi.spyOn(apiClient, "renameTenant").mockResolvedValue(makeTenant({ name: "Lab", is_owner: true }));
      const user = userEvent.setup();
      await renderPage();

      const field = await screen.findByLabelText("Name");
      expect(field).toHaveValue("Ada's workspace");
      expect(screen.getByRole("button", { name: "Rename" })).toBeDisabled();
      await user.clear(field);
      await user.type(field, "Lab");
      await user.click(screen.getByRole("button", { name: "Rename" }));

      expect(apiClient.renameTenant).toHaveBeenCalledWith("Lab");
      await vi.waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Lab"));
    });

    it("shows the server's refusal", async () => {
      stub({ tenant: makeTenant({ name: "Ada's workspace", is_owner: true }) });
      vi.spyOn(apiClient, "renameTenant").mockRejectedValue(
        new ApiError(409, "you already belong to a workspace with that name"),
      );
      const user = userEvent.setup();
      await renderPage();

      await user.type(await screen.findByLabelText("Name"), "2");
      await user.click(screen.getByRole("button", { name: "Rename" }));

      expect(await screen.findByText(/already belong to a workspace with that name/)).toBeInTheDocument();
    });

    it("shows a member the name, read-only", async () => {
      stub({ tenant: makeTenant({ name: "Ada's workspace", is_owner: false }) });
      await renderPage();

      const tenant = within(await section("This tenant"));
      expect(await tenant.findByText("Ada's workspace")).toBeInTheDocument();
      expect(tenant.queryByLabelText("Name")).not.toBeInTheDocument();
      expect(tenant.queryByRole("button", { name: "Rename" })).not.toBeInTheDocument();
    });
  });
});
