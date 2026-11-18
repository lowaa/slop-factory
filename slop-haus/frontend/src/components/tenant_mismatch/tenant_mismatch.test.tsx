import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeTenant } from "@/test/fixtures";
import TenantMismatch from "./tenant_mismatch";

const TENANTS = [
  makeTenant({ id: "tenant-a", name: "Alpha", current: true }),
  makeTenant({ id: "tenant-b", name: "Beta", current: false }),
];

describe("TenantMismatch", () => {
  it("says where the ticket is and where the session is", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);

    render(<TenantMismatch ticketId={4} tenantId="tenant-b" back={<a href="/">back</a>} />);

    expect(await screen.findByRole("button", { name: "Switch to Beta" })).toBeInTheDocument();
    expect(screen.getByText(/but you are working in/)).toHaveTextContent(
      "This link is to ticket #4 in Beta, but you are working in Alpha.",
    );
    expect(screen.getByRole("link", { name: "back" })).toBeInTheDocument();
  });

  it("stays put, saying why, when the switch is refused", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue(TENANTS);
    vi.spyOn(apiClient, "switchTenant").mockRejectedValue(new ApiError(403, "not a member of that tenant"));
    const reloadPage = vi.spyOn(navigation, "reloadPage").mockImplementation(() => {});
    const user = userEvent.setup();

    render(<TenantMismatch ticketId={4} tenantId="tenant-b" back={null} />);
    await user.click(await screen.findByRole("button", { name: "Switch to Beta" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not a member of that tenant");
    expect(reloadPage).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Switch to Beta" })).toBeEnabled();
  });

  it("offers no switch when the tenants cannot be loaded", async () => {
    vi.spyOn(apiClient, "listTenants").mockRejectedValue(new ApiError(500, "boom"));

    render(<TenantMismatch ticketId={4} tenantId="tenant-b" back={null} />);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(screen.queryByRole("button", { name: /^Switch to/ })).not.toBeInTheDocument();
  });
});
