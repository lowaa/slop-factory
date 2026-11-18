import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeTenant } from "@/test/fixtures";
import TenantChanged from "./tenant_changed";

const TENANTS = [
  makeTenant({ id: "tenant-a", name: "Alpha", current: false }),
  makeTenant({ id: "tenant-b", name: "Beta", current: true }),
];

describe("TenantChanged", () => {
  it("says which tenant the tab was on and which the session is on", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);

    render(<TenantChanged was="tenant-a" />);

    expect(await screen.findByRole("button", { name: "Reload into Beta" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Switch back to Alpha" })).toBeInTheDocument();
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      "This tab was on Alpha. Your session is now on Beta, switched in another tab.",
    );
  });

  it("reloads into the session's tenant without switching", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);
    const switchTenant = vi.spyOn(apiClient, "switchTenant");
    const reloadPage = vi.spyOn(navigation, "reloadPage").mockImplementation(() => {});

    render(<TenantChanged was="tenant-a" />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Reload into Beta" }));

    expect(reloadPage).toHaveBeenCalledTimes(1);
    expect(switchTenant).not.toHaveBeenCalled();
  });

  it("switches back to the tab's tenant, then reloads", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);
    const switchTenant = vi.spyOn(apiClient, "switchTenant").mockResolvedValue({} as never);
    const reloadPage = vi.spyOn(navigation, "reloadPage").mockImplementation(() => {});

    render(<TenantChanged was="tenant-a" />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Switch back to Alpha" }));

    expect(switchTenant).toHaveBeenCalledWith("tenant-a");
    expect(reloadPage).toHaveBeenCalledTimes(1);
  });

  it("stays put, saying why, when switching back is refused", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);
    vi.spyOn(apiClient, "switchTenant").mockRejectedValue(new ApiError(403, "not a member of that tenant"));
    const reloadPage = vi.spyOn(navigation, "reloadPage").mockImplementation(() => {});

    render(<TenantChanged was="tenant-a" />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Switch back to Alpha" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not a member of that tenant");
    expect(reloadPage).not.toHaveBeenCalled();
  });

  it("still offers both actions when the tenants cannot be listed", async () => {
    vi.spyOn(apiClient, "listTenants").mockRejectedValue(new ApiError(500, "boom"));

    render(<TenantChanged was="tenant-a" />);

    expect(await screen.findByRole("button", { name: "Reload" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Switch back" })).toBeEnabled();
  });
});
