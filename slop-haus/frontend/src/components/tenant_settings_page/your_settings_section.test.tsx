import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, type TenantSettingChange, type UserSetting } from "@/api";
import YourSettingsSection from "./your_settings_section";

function bool(name: string, value: boolean | null): UserSetting {
  return { name, type: "bool", str_val: null, int_val: null, float_val: null, bool_val: value, is_default: value === null, updated_at: null };
}

describe("YourSettingsSection render markdown", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue(bool("render_markdown", false));
  });

  it("shows the switch on by default", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([bool("render_markdown", null)]);
    render(<YourSettingsSection />);
    await waitFor(() => expect(apiClient.listMySettings).toHaveBeenCalled());
    expect(screen.getByRole("switch", { name: "Render markdown" })).toBeChecked();
  });

  it("saves turning it off", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    render(<YourSettingsSection />);
    await userEvent.click(screen.getByRole("switch", { name: "Render markdown" }));
    expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith("render_markdown", false);
    await waitFor(() => expect(screen.getByRole("switch", { name: "Render markdown" })).not.toBeChecked());
  });
});

function change(id: number, on: boolean, setAt: string): TenantSettingChange {
  return { id, type: "bool", bool_val: on, int_val: null, float_val: null, str_val: null, set_at: setAt, earlier_membership: false };
}

describe("YourSettingsSection history", () => {
  const NAME = "approvals.auto.status";

  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([bool(NAME, false)]);
    vi.spyOn(apiClient, "getSettingHistory");
  });

  async function openHistory() {
    render(<YourSettingsSection />);
    await userEvent.click(await screen.findByRole("button", { name: /^History of / }));
  }

  it("reads the per-user endpoint and lists changes newest first with the old value", async () => {
    vi.spyOn(apiClient, "getMyTenantSettingHistory").mockResolvedValue({
      changes: [change(2, true, "2026-10-12T08:00:00Z"), change(1, false, "2026-10-11T08:00:00Z")],
    });
    await openHistory();
    const items = within(await screen.findByRole("list", { name: "Changes" })).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("off → on");
    expect(items[1]).toHaveTextContent("set to off");
    expect(apiClient.getMyTenantSettingHistory).toHaveBeenCalledWith(NAME, { limit: 20 });
    expect(apiClient.getSettingHistory).not.toHaveBeenCalled();
  });

  it("pages with the cursor", async () => {
    const get = vi
      .spyOn(apiClient, "getMyTenantSettingHistory")
      .mockResolvedValueOnce({ changes: [change(3, true, "2026-10-13T08:00:00Z")], next_before: 3 })
      .mockResolvedValueOnce({ changes: [change(2, false, "2026-10-12T08:00:00Z")] });
    await openHistory();
    await userEvent.click(await screen.findByRole("button", { name: "Show older changes" }));
    await waitFor(() => expect(get).toHaveBeenLastCalledWith(NAME, { limit: 20, before: 3 }));
    const items = within(await screen.findByRole("list", { name: "Changes" })).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("off → on");
    expect(screen.queryByRole("button", { name: "Show older changes" })).toBeNull();
  });

  it("says so when there are no changes", async () => {
    vi.spyOn(apiClient, "getMyTenantSettingHistory").mockResolvedValue({ changes: [] });
    await openHistory();
    expect(await screen.findByText("No changes yet")).toBeVisible();
  });

  it("does not infer an old value across a membership reset", async () => {
    vi.spyOn(apiClient, "getMyTenantSettingHistory").mockResolvedValue({
      changes: [change(2, true, "2026-10-12T08:00:00Z"), { ...change(1, false, "2026-10-01T08:00:00Z"), earlier_membership: true }],
    });
    await openHistory();
    const items = within(await screen.findByRole("list", { name: "Changes" })).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("set to on");
    expect(items[1]).toHaveTextContent("before you last joined this tenant");
  });

  it("reloads the history after the setting is written", async () => {
    const get = vi
      .spyOn(apiClient, "getMyTenantSettingHistory")
      .mockResolvedValueOnce({ changes: [] })
      .mockResolvedValueOnce({ changes: [change(1, true, "2026-10-12T08:00:00Z")] });
    vi.spyOn(apiClient, "setMyTenantBoolSetting").mockResolvedValue({
      ...bool(NAME, true),
      updated_at: "2026-10-12T08:00:00Z",
    });
    await openHistory();
    expect(await screen.findByText("No changes yet")).toBeVisible();
    await userEvent.click(screen.getByRole("switch", { name: "Parking or cancelling a ticket" }));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("set to on")).toBeVisible();
  });
});
