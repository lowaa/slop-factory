import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { defaultTenantColour } from "@/lib/tenant_colour";
import { makeProfile, makeTenant } from "@/test/fixtures";
import TenantSwitcher from "./tenant_switcher";

describe("TenantSwitcher", () => {
  let reloadSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([]);
    reloadSpy = vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders nothing while loading and once it knows there is only one tenant", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ current: true })]);
    const { container } = render(<TenantSwitcher />);

    expect(container).toBeEmptyDOMElement();
    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when the fetch fails, rather than showing an error in the header", async () => {
    vi.spyOn(apiClient, "listTenants").mockRejectedValue(new ApiError(500, "boom"));
    const { container } = render(<TenantSwitcher />);

    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("shows a picker naming the current tenant when there is more than one", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: "t1", name: "Ada's workspace", current: true }),
      makeTenant({ id: "t2", name: "Bea's workspace", current: false }),
    ]);
    render(<TenantSwitcher />);

    expect(await screen.findByRole("combobox", { name: "Tenant" })).toHaveTextContent("Ada's workspace");
  });

  it("switches tenant and reloads to the board", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: "t1", name: "Ada's workspace", current: true }),
      makeTenant({ id: "t2", name: "Bea's workspace", current: false }),
    ]);
    vi.spyOn(apiClient, "switchTenant").mockResolvedValue(makeProfile());
    const user = userEvent.setup();
    render(<TenantSwitcher />);

    await user.click(await screen.findByRole("combobox", { name: "Tenant" }));
    await user.click(await screen.findByRole("option", { name: /Bea.s workspace/ }));

    expect(apiClient.switchTenant).toHaveBeenCalledWith("t2");
    await vi.waitFor(() => expect(reloadSpy).toHaveBeenCalled());
  });

  it("shows an error and does not reload when switching fails", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: "t1", name: "Ada's workspace", current: true }),
      makeTenant({ id: "t2", name: "Bea's workspace", current: false }),
    ]);
    vi.spyOn(apiClient, "switchTenant").mockRejectedValue(new ApiError(403, "not a member of that tenant"));
    const user = userEvent.setup();
    render(<TenantSwitcher />);

    await user.click(await screen.findByRole("combobox", { name: "Tenant" }));
    await user.click(await screen.findByRole("option", { name: /Bea.s workspace/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not a member of that tenant");
    expect(reloadSpy).not.toHaveBeenCalled();
  });

  it("lists each tenant with its colour badge, name and owner", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: "t1", name: "Ada's workspace", current: true, colour: "violet", owners: ["Ada"] }),
      makeTenant({ id: "t2", name: "Lab", current: false, colour: null, owners: ["Ada", "Bea"] }),
      makeTenant({ id: "t3", name: "Ownerless", current: false }),
    ]);
    const user = userEvent.setup();
    render(<TenantSwitcher />);

    const trigger = await screen.findByRole("combobox", { name: "Tenant" });
    expect(trigger.querySelector("[data-colour=violet]")).toHaveAttribute("data-initials", "AW");
    expect(trigger).toHaveTextContent(/^Ada.s workspace$/);
    await user.click(trigger);

    const ada = screen.getByRole("option", { name: /^Ada.s workspace\s*owner: Ada$/ });
    expect(ada.querySelector("[data-colour=violet]")).not.toBeNull();
    const lab = screen.getByRole("option", { name: /^Lab\s*owners: Ada, Bea$/ });
    expect(lab.querySelector("[data-colour]")).toHaveAttribute("data-colour", defaultTenantColour("t2"));
    expect(screen.getByRole("option", { name: "Ownerless" })).toBeInTheDocument();
  });

  describe("archived tenants", () => {
    const active = [makeTenant({ id: "t1", name: "Ada's workspace", current: true }), makeTenant({ id: "t2", name: "Lab", current: false })];
    const archivedLab = makeTenant({ id: "t9", name: "Old lab", current: false, archived_at: "2026-10-01T00:00:00Z" });

    it("keeps them out of the picker and offers them behind Show archived (N)", async () => {
      vi.spyOn(apiClient, "listTenants").mockResolvedValue(active);
      vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([archivedLab]);
      const user = userEvent.setup();
      render(<TenantSwitcher />);

      const trigger = await screen.findByRole("combobox", { name: "Tenant" });
      await user.click(trigger);
      expect(screen.queryByRole("option", { name: /Old lab/ })).not.toBeInTheDocument();
      await user.keyboard("{Escape}");
      expect(screen.queryByText("Old lab")).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Show archived (1)" }));
      expect(within(screen.getByRole("group", { name: "Archived tenants" })).getByText("Old lab")).toBeInTheDocument();
    });

    it("shows no toggle for someone with nothing archived", async () => {
      vi.spyOn(apiClient, "listTenants").mockResolvedValue(active);
      render(<TenantSwitcher />);

      await screen.findByRole("combobox", { name: "Tenant" });
      expect(screen.queryByRole("button", { name: /archived/ })).not.toBeInTheDocument();
    });

    it("is there for an owner with one active tenant and an archived one", async () => {
      vi.spyOn(apiClient, "listTenants").mockResolvedValue([active[0]]);
      vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([archivedLab]);
      render(<TenantSwitcher />);

      expect(await screen.findByRole("button", { name: "Show archived (1)" })).toBeInTheDocument();
    });

    it("unarchives one and lists the tenants again", async () => {
      const listTenants = vi.spyOn(apiClient, "listTenants").mockResolvedValue(active);
      const listArchived = vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([archivedLab]);
      const unarchive = vi.spyOn(apiClient, "unarchiveTenant").mockImplementation(async () => {
        listTenants.mockResolvedValue([...active, { ...archivedLab, archived_at: undefined }]);
        listArchived.mockResolvedValue([]);
      });
      const user = userEvent.setup();
      render(<TenantSwitcher />);
      await user.click(await screen.findByRole("button", { name: "Show archived (1)" }));
      await user.click(screen.getByRole("button", { name: "Unarchive Old lab" }));

      expect(unarchive).toHaveBeenCalledWith("t9");
      await vi.waitFor(() => expect(screen.queryByRole("button", { name: /archived/ })).not.toBeInTheDocument());
      await user.click(screen.getByRole("combobox", { name: "Tenant" }));
      expect(await screen.findByRole("option", { name: /Old lab/ })).toBeInTheDocument();
    });

    it("shows the over-the-limit refusal and keeps it archived", async () => {
      vi.spyOn(apiClient, "listTenants").mockResolvedValue(active);
      vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([archivedLab]);
      vi.spyOn(apiClient, "unarchiveTenant").mockRejectedValue(
        new ApiError(403, "you already own the most active tenants allowed", "tenant_limit_reached"),
      );
      const user = userEvent.setup();
      render(<TenantSwitcher />);
      await user.click(await screen.findByRole("button", { name: "Show archived (1)" }));
      await user.click(screen.getByRole("button", { name: "Unarchive Old lab" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("most active tenants allowed");
      expect(screen.getByRole("button", { name: "Hide archived (1)" })).toBeInTheDocument();
    });
  });
});
