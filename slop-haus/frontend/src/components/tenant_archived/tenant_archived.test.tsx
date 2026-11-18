import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeProfile, makeTenant } from "@/test/fixtures";
import TenantArchived from "./tenant_archived";

describe("TenantArchived", () => {
  it("says the tenant was archived and offers the tenants left, then reloads into the one chosen", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ id: "t2", name: "Lab", current: false })]);
    vi.spyOn(apiClient, "switchTenant").mockResolvedValue(makeProfile());
    const reload = vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
    render(<TenantArchived />);

    expect(screen.getByRole("alertdialog")).toHaveTextContent("This tenant was archived");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Switch to Lab" }));

    expect(apiClient.switchTenant).toHaveBeenCalledWith("t2");
    await vi.waitFor(() => expect(reload).toHaveBeenCalled());
  });

  it("says so when there is no other tenant", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([]);
    render(<TenantArchived />);

    expect(await screen.findByText("You have no other tenant to switch to.")).toBeInTheDocument();
  });

  it("shows a refused switch", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ id: "t2", name: "Lab", current: false })]);
    vi.spyOn(apiClient, "switchTenant").mockRejectedValue(new ApiError(403, "not a member of that tenant"));
    const reload = vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
    render(<TenantArchived />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Switch to Lab" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not a member of that tenant");
    expect(reload).not.toHaveBeenCalled();
  });
});
