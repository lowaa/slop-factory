import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { defaultTenantColour } from "@/lib/tenant_colour";
import { makeTenant } from "@/test/fixtures";
import TenantColourRow from "./tenant_colour_row";

afterEach(() => vi.restoreAllMocks());

describe("TenantColourRow", () => {
  it("lets an owner pick a colour, saves it and refreshes the tenants", async () => {
    vi.spyOn(apiClient, "listTenants")
      .mockResolvedValueOnce([makeTenant({ is_owner: true })])
      .mockResolvedValue([makeTenant({ is_owner: true, colour: "blue" })]);
    vi.spyOn(apiClient, "setTenantColour").mockResolvedValue(makeTenant({ is_owner: true, colour: "blue" }));
        const user = userEvent.setup();
    render(<TenantColourRow />);

    expect(await screen.findByRole("radio", { name: "Default" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Save colour" })).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: "Blue" }));
    await user.click(screen.getByRole("button", { name: "Save colour" }));

    expect(apiClient.setTenantColour).toHaveBeenCalledWith("blue");
    expect(apiClient.listTenants).toHaveBeenCalledTimes(2);
    expect(await screen.findByRole("button", { name: "Save colour" })).toBeDisabled();
  });

  it("clears the colour with Default", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: true, colour: "red" })]);
    vi.spyOn(apiClient, "setTenantColour").mockResolvedValue(makeTenant({ is_owner: true, colour: null }));
    const user = userEvent.setup();
    render(<TenantColourRow />);

    expect(await screen.findByRole("radio", { name: "Red" })).toBeChecked();
    await user.click(screen.getByRole("radio", { name: "Default" }));
    await user.click(screen.getByRole("button", { name: "Save colour" }));

    expect(apiClient.setTenantColour).toHaveBeenCalledWith(null);
  });

  it("previews the stripe the default would give", async () => {
    const tenant = makeTenant({ is_owner: true });
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([tenant]);
    render(<TenantColourRow />);

    const preview = await screen.findByLabelText("Preview");
    expect(preview.querySelector(".tenant-colour__stripe")).toHaveAttribute("data-colour", defaultTenantColour(tenant.id));
  });

  it("shows the server's refusal", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: true })]);
    vi.spyOn(apiClient, "setTenantColour").mockRejectedValue(new ApiError(403, "only a tenant's owner can do this"));
    const user = userEvent.setup();
    render(<TenantColourRow />);

    await user.click(await screen.findByRole("radio", { name: "Teal" }));
    await user.click(screen.getByRole("button", { name: "Save colour" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("only a tenant's owner");
  });

  it("shows a member the colour without a way to change it", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: false, colour: "amber" })]);
    render(<TenantColourRow />);

    expect(await screen.findByText(/only an owner/i)).toBeInTheDocument();
    expect(screen.queryByRole("radio")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save colour" })).toBeNull();
  });
});
