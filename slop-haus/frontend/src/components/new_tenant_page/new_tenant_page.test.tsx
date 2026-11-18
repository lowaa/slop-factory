import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeProfile, makeTenant } from "@/test/fixtures";
import NewTenantPage from "./new_tenant_page";

const owned = (n: number) =>
  Array.from({ length: n }, (_, i) =>
    makeTenant({ id: `00000000-0000-0000-0000-00000000000${i}`, name: `T${i}`, is_owner: true, current: i === 0 }),
  );

describe("NewTenantPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows how many of the limit are used, even for a single-tenant user, and creates", async () => {
    vi.spyOn(apiClient, "tenantOverview").mockResolvedValue({ tenants: owned(1), ownerLimit: 3 });
    const create = vi.spyOn(apiClient, "createTenant").mockResolvedValue(makeProfile());
    const reload = vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
    render(<NewTenantPage />);

    expect(await screen.findByText(/You own 1 of 3/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Tenant name"), "Side project");
    await userEvent.click(screen.getByRole("button", { name: "Create tenant" }));

    expect(create).toHaveBeenCalledWith("Side project");
    expect(reload).toHaveBeenCalled();
  });

  it("does not count tenants the person only joined", async () => {
    vi.spyOn(apiClient, "tenantOverview").mockResolvedValue({
      tenants: [...owned(1), makeTenant({ id: "99999999-9999-9999-9999-999999999999", is_owner: false, current: false })],
      ownerLimit: 3,
    });
    render(<NewTenantPage />);
    expect(await screen.findByText(/You own 1 of 3/)).toBeInTheDocument();
  });

  it("says why it is unavailable at the limit, with the count", async () => {
    vi.spyOn(apiClient, "tenantOverview").mockResolvedValue({ tenants: owned(3), ownerLimit: 3 });
    render(<NewTenantPage />);
    expect(await screen.findByRole("status")).toHaveTextContent("You own 3 of 3");
    expect(screen.getByRole("status")).toHaveTextContent("Ask an admin");
    expect(screen.queryByRole("button", { name: "Create tenant" })).not.toBeInTheDocument();
  });

  it("says so when creation is switched off", async () => {
    vi.spyOn(apiClient, "tenantOverview").mockResolvedValue({ tenants: owned(1), ownerLimit: 0 });
    render(<NewTenantPage />);
    expect(await screen.findByRole("status")).toHaveTextContent("turned off");
  });

  it("shows the server's refusal of a name and stays on the form", async () => {
    vi.spyOn(apiClient, "tenantOverview").mockResolvedValue({ tenants: owned(1), ownerLimit: 3 });
    vi.spyOn(apiClient, "createTenant").mockRejectedValue(
      new ApiError(409, "you already belong to a workspace with that name"),
    );
    const reload = vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
    render(<NewTenantPage />);
    await userEvent.type(await screen.findByLabelText("Tenant name"), "T0");
    await userEvent.click(screen.getByRole("button", { name: "Create tenant" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("already belong");
    expect(reload).not.toHaveBeenCalled();
  });
});
