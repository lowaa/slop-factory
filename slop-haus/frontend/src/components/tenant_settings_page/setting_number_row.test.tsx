import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeProfile, makeSetting, makeTenant, makeTenantUserSetting } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import TenantSettingsPage from "./tenant_settings_page";
import SettingNumberRow from "./setting_number_row";
import SettingMinutesRow from "./setting_minutes_row";

const BATCH = "Merge queue: changes landed per CI run";

function stubPage(isOwner: boolean, intVal = 1) {
  vi.spyOn(apiClient, "listSettings").mockResolvedValue([
    makeSetting({ name: "merge_queue_batch_size", type: "int", bool_val: null, int_val: intVal }),
  ]);
  vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([makeTenantUserSetting()]);
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: isOwner })]);
  vi.spyOn(apiClient, "getWorkspace").mockResolvedValue({ manifest: null, can_edit: true, proposal: null, agents: [], divergences: [] });
  vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue({
    merge_method: "merge_queue",
    merge_queue_enabled: true,
    change_tools: true,
    merge_queue_available: true,
  });
}

describe("SettingNumberRow", () => {
  it("saves a value within bounds", async () => {
    const onWrite = vi.fn().mockResolvedValue(undefined);
    await renderSignedIn(<SettingNumberRow label={BATCH} value={1} min={1} max={3} onWrite={onWrite} />);
    const field = screen.getByRole("spinbutton", { name: BATCH });
    await userEvent.clear(field);
    await userEvent.type(field, "3");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onWrite).toHaveBeenCalledWith(3);
  });

  it("refuses an out-of-range value, naming the range", async () => {
    const onWrite = vi.fn().mockResolvedValue(undefined);
    await renderSignedIn(<SettingNumberRow label={BATCH} value={1} min={1} max={3} onWrite={onWrite} />);
    const field = screen.getByRole("spinbutton", { name: BATCH });
    await userEvent.clear(field);
    await userEvent.type(field, "4");
    expect(screen.getByRole("alert")).toHaveTextContent("Enter a whole number from 1 to 3.");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onWrite).not.toHaveBeenCalled();
  });

  it.each(["1.5", ""])("refuses %j, which is not a whole number", async (typed) => {
    const onWrite = vi.fn().mockResolvedValue(undefined);
    await renderSignedIn(<SettingNumberRow label={BATCH} value={1} min={1} max={3} onWrite={onWrite} />);
    const field = screen.getByRole("spinbutton", { name: BATCH });
    await userEvent.clear(field);
    if (typed) await userEvent.type(field, typed);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(onWrite).not.toHaveBeenCalled();
  });

  it("keeps the minutes unit and range on the minutes wrapper", async () => {
    await renderSignedIn(
      <SettingMinutesRow label="Wait" value={60} min={5} max={1440} onWrite={vi.fn()}>
        <p>meta</p>
      </SettingMinutesRow>,
    );
    expect(screen.getByText("minutes (5–1440)")).toBeInTheDocument();
    expect(screen.getByText("meta")).toBeInTheDocument();
  });

  it("shows the server's refusal and keeps the stored value", async () => {
    const onWrite = vi.fn().mockRejectedValue(new ApiError(400, "merge queue batch size must be 1..3"));
    await renderSignedIn(<SettingNumberRow label={BATCH} value={1} min={1} max={3} onWrite={onWrite} />);
    const field = screen.getByRole("spinbutton", { name: BATCH });
    await userEvent.clear(field);
    await userEvent.type(field, "2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("1..3");
    expect(field).toHaveValue(1);
  });
});

describe("merge_queue_batch_size on the tenant settings page", () => {
  it("is editable by an owner and writes an int", async () => {
    stubPage(true);
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(
      makeSetting({ name: "merge_queue_batch_size", type: "int", bool_val: null, int_val: 2 }),
    );
    await renderSignedIn(<TenantSettingsPage />, { profile: makeProfile() });
    const field = await screen.findByRole("spinbutton", { name: BATCH });
    await userEvent.clear(field);
    await userEvent.type(field, "2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledWith("merge_queue_batch_size", { type: "int", int_val: 2 });
  });

  it("is read-only to a non-owner", async () => {
    stubPage(false, 2);
    await renderSignedIn(<TenantSettingsPage />, { profile: makeProfile() });
    expect(await screen.findByText(/only an owner of the tenant can change this/i)).toBeInTheDocument();
    expect(screen.queryByRole("spinbutton")).toBeNull();
  });
});
