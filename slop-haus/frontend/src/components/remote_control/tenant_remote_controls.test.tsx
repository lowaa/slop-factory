import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeConductor, makeConductorDirectoryEntry, makeGrant, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import TenantRemoteControls from "./tenant_remote_controls";

const OFF = "2026-01-02T00:00:00Z";
const mine = makeConductor({ id: "c-mine", name: "my-laptop" });
const myEntry = makeConductorDirectoryEntry({ id: "c-mine", name: "my-laptop", owner_display_name: "Ada", has_live_session: true });
const theirs = makeConductorDirectoryEntry({ id: "c-theirs", name: "olive-box", owner_display_name: "Olive", host_seen_at: null });

function serve({ isOwner = false, entries = [myEntry, theirs] } = {}) {
  vi.spyOn(apiClient, "conductorDirectory").mockResolvedValue(entries);
  vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [mine], others: [] });
  vi.spyOn(apiClient, "listGrants").mockResolvedValue([
    makeGrant({ id: "g1", agent_id: "c-mine", agent_kind: "conductor", client_name: "Host Client" }),
    makeGrant({ id: "g2", agent_id: "c-theirs", agent_kind: "conductor", client_name: "Their Client" }),
  ]);
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: isOwner })]);
}

const row = async (name: string) => (await screen.findByText(name)).closest("li") as HTMLElement;

describe("TenantRemoteControls", () => {
  beforeEach(() => serve());
  afterEach(() => vi.restoreAllMocks());

  it("lists every remote control with owner, last connected and live session", async () => {
    await renderSignedIn(<TenantRemoteControls asView />);

    const mineRow = await row("my-laptop");
    expect(within(mineRow).getByText("Ada (you)")).toBeInTheDocument();
    expect(within(mineRow).getByText("Live session")).toBeInTheDocument();
    expect(within(mineRow).getByText(/^Last connected /)).toBeInTheDocument();
    const theirRow = await row("olive-box");
    expect(within(theirRow).getByText("Olive")).toBeInTheDocument();
    expect(within(theirRow).getByText("Never connected")).toBeInTheDocument();
    expect(within(theirRow).queryByText("Live session")).not.toBeInTheDocument();
  });

  it("gives a member actions on their own only", async () => {
    await renderSignedIn(<TenantRemoteControls asView />);

    expect(within(await row("my-laptop")).getAllByRole("button").map((b) => b.textContent)).toEqual(["Disable", "Remove", "Revoke"]);
    expect(within(await row("olive-box")).queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows a manageable remote control's authorizations, so they can be revoked, and none for others'", async () => {
    await renderSignedIn(<TenantRemoteControls asView />);

    expect(within(await row("my-laptop")).getByText("Host Client")).toBeInTheDocument();
    expect(within(await row("olive-box")).queryByText("Their Client")).not.toBeInTheDocument();
  });

  it("gives the tenant's owner actions on everyone's", async () => {
    serve({ isOwner: true });
    await renderSignedIn(<TenantRemoteControls asView />);

    expect(within(await row("olive-box")).getAllByRole("button").map((b) => b.textContent)).toEqual(["Disable", "Remove", "Revoke"]);
  });

  it("disables through the conductor route and shows the directory read again", async () => {
    const user = userEvent.setup();
    const disable = vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue({ ...mine, disabled_at: OFF });
    vi.spyOn(apiClient, "conductorDirectory")
      .mockResolvedValueOnce([myEntry, theirs])
      .mockResolvedValue([{ ...myEntry, disabled_at: OFF }, theirs]);
    await renderSignedIn(<TenantRemoteControls asView />);

    await user.click(within(await row("my-laptop")).getByRole("button", { name: "Disable" }));

    expect(disable).toHaveBeenCalledWith("c-mine", true);
    expect(await within(await row("my-laptop")).findByRole("button", { name: "Enable" })).toBeInTheDocument();
    expect(within(await row("my-laptop")).getByText("Disabled")).toBeInTheDocument();
  });

  it("removes through the conductor routes after asking, and restores", async () => {
    const user = userEvent.setup();
    const disable = vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue({ ...mine, disabled_at: OFF });
    const archive = vi.spyOn(apiClient, "archiveConductor").mockResolvedValue({ ...mine, disabled_at: OFF, archived_at: OFF });
    const unarchive = vi.spyOn(apiClient, "unarchiveConductor").mockResolvedValue({ ...mine, disabled_at: OFF });
    vi.spyOn(apiClient, "conductorDirectory")
      .mockResolvedValueOnce([myEntry])
      .mockResolvedValueOnce([{ ...myEntry, disabled_at: OFF, archived_at: OFF }])
      .mockResolvedValue([{ ...myEntry, disabled_at: OFF }]);
    await renderSignedIn(<TenantRemoteControls asView />);

    await user.click(within(await row("my-laptop")).getByRole("button", { name: "Remove" }));
    expect(archive).not.toHaveBeenCalled();
    await user.click(within(screen.getByRole("group", { name: "Confirm removing my-laptop" })).getByRole("button", { name: "Remove" }));
    expect(disable).toHaveBeenCalledWith("c-mine", true);
    expect(archive).toHaveBeenCalledWith("c-mine");

    await user.click(await screen.findByRole("checkbox", { name: "Show removed (1)" }));
    await user.click(within(await row("my-laptop")).getByRole("button", { name: "Restore" }));
    expect(unarchive).toHaveBeenCalledWith("c-mine");
    expect(await within(await row("my-laptop")).findByText("Disabled")).toBeInTheDocument();
  });

  describe("one a supervisor runs", () => {
    const RUN_BY = "s-1";

    it("is removed by its owner through that supervisor, not archived, and is not offered back", async () => {
      const user = userEvent.setup();
      const runEntry = { ...myEntry, run_by_supervisor_id: RUN_BY };
      vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue({ ...mine, disabled_at: OFF, run_by_supervisor_id: RUN_BY });
      const archive = vi.spyOn(apiClient, "archiveConductor");
      const remove = vi.spyOn(apiClient, "removeOwnRemoteControl").mockResolvedValue(undefined);
      vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [{ ...mine, disabled_at: OFF, archived_at: OFF, run_by_supervisor_id: RUN_BY }], others: [] });
      vi.spyOn(apiClient, "conductorDirectory")
        .mockResolvedValueOnce([runEntry])
        .mockResolvedValue([{ ...runEntry, disabled_at: OFF, archived_at: OFF }]);
      await renderSignedIn(<TenantRemoteControls asView />);

      await user.click(within(await row("my-laptop")).getByRole("button", { name: "Remove" }));
      await user.click(within(screen.getByRole("group", { name: "Confirm removing my-laptop" })).getByRole("button", { name: "Remove" }));

      await vi.waitFor(() => expect(remove).toHaveBeenCalledWith(RUN_BY, "my-laptop"));
      expect(archive).not.toHaveBeenCalled();
      await user.click(await screen.findByRole("checkbox", { name: "Show removed (1)" }));
      expect(within(await row("my-laptop")).queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
    });

    it("gives the tenant's owner Disable on someone else's, but no Remove the server would refuse", async () => {
      serve({ isOwner: true, entries: [myEntry, { ...theirs, run_by_supervisor_id: RUN_BY }] });
      await renderSignedIn(<TenantRemoteControls asView />);

      expect(within(await row("olive-box")).getAllByRole("button").map((b) => b.textContent)).toEqual(["Disable", "Revoke"]);
    });
  });

  it("keeps removed ones behind Show removed", async () => {
    const user = userEvent.setup();
    serve({ entries: [myEntry, { ...theirs, disabled_at: OFF, archived_at: OFF }] });
    await renderSignedIn(<TenantRemoteControls asView />);

    await row("my-laptop");
    expect(screen.queryByText("olive-box")).not.toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Show removed (1)" }));
    expect(within(await row("olive-box")).getByText("Removed")).toBeInTheDocument();
  });

  it("says so when there are none, and when the read fails", async () => {
    serve({ entries: [] });
    const { unmount } = await renderSignedIn(<TenantRemoteControls asView />);
    expect(await screen.findByText("No one in this tenant has a remote control yet.")).toBeInTheDocument();
    unmount();

    vi.spyOn(apiClient, "conductorDirectory").mockRejectedValue(new Error("boom"));
    await renderSignedIn(<TenantRemoteControls asView />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("shows no session titles or content for anyone's remote control, and reads no sessions", async () => {
    const sessions = vi.spyOn(apiClient, "listConversations");
    // What the directory must never carry; if it did, the page must not show it.
    const leaky = { ...theirs, first_line: "secret plan for the launch", title: "secret plan for the launch" } as typeof theirs;
    serve({ entries: [myEntry, leaky] });
    const { container } = await renderSignedIn(<TenantRemoteControls asView />);

    await row("olive-box");
    expect(container).not.toHaveTextContent("secret plan");
    expect(sessions).not.toHaveBeenCalled();
  });
});
