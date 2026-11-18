import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeProfile, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import ArchiveTenantSection from "./archive_tenant_section";

const TENANT = makeTenant({ id: "t1", name: "Ada's workspace", is_owner: true });

function stub(tenant = TENANT) {
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([tenant]);
  return vi.spyOn(navigation, "reloadToBoard").mockImplementation(() => {});
}

async function openConfirm(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: /Archive this tenant/ }));
}

async function typeName(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.type(screen.getByLabelText("Type the tenant's name to confirm"), name);
}

describe("ArchiveTenantSection", () => {
  it("is not offered to a member who is not an owner", async () => {
    stub(makeTenant({ is_owner: false }));
    await renderSignedIn(<ArchiveTenantSection />, { profile: makeProfile() });

    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /Archive/ })).not.toBeInTheDocument();
  });

  it("states what archiving does and keeps the button off until the exact name is typed", async () => {
    stub();
    const user = userEvent.setup();
    await renderSignedIn(<ArchiveTenantSection />);
    await openConfirm(user);

    const form = screen.getByRole("form", { name: "Archive Ada's workspace" });
    expect(form).toHaveTextContent(/agent .* switched off/i);
    expect(form).toHaveTextContent(/leases and integration lock/i);
    expect(form).toHaveTextContent(/invites .* revoked/i);
    expect(form).toHaveTextContent(/lose access/i);
    expect(form).toHaveTextContent(/nothing is deleted/i);
    expect(form).toHaveTextContent(/unarchive/i);
    const archive = screen.getByRole("button", { name: "Archive tenant" });
    expect(archive).toBeDisabled();
    await typeName(user, "ada's workspace");
    expect(archive).toBeDisabled();
    await user.clear(screen.getByLabelText("Type the tenant's name to confirm"));
    await typeName(user, "Ada's workspace");
    expect(archive).toBeEnabled();
  });

  it("archives with the typed name, then reloads into the tenant the session moved to", async () => {
    const reload = stub();
    const archive = vi.spyOn(apiClient, "archiveTenant").mockResolvedValue(makeProfile());
    const user = userEvent.setup();
    await renderSignedIn(<ArchiveTenantSection />);
    await openConfirm(user);
    await typeName(user, "Ada's workspace");
    await user.click(screen.getByRole("button", { name: "Archive tenant" }));

    expect(archive).toHaveBeenCalledWith("t1", "Ada's workspace");
    await vi.waitFor(() => expect(reload).toHaveBeenCalled());
  });

  it("shows the refusal for the last active tenant and does not reload", async () => {
    const reload = stub();
    vi.spyOn(apiClient, "archiveTenant").mockRejectedValue(
      new ApiError(409, "this is your only active tenant; create another first", "last_active_tenant"),
    );
    const user = userEvent.setup();
    await renderSignedIn(<ArchiveTenantSection />);
    await openConfirm(user);
    await typeName(user, "Ada's workspace");
    await user.click(screen.getByRole("button", { name: "Archive tenant" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("only active tenant");
    expect(reload).not.toHaveBeenCalled();
  });

  it("steps up on reauth_required, then archives again with the same name", async () => {
    const reload = stub();
    const archive = vi
      .spyOn(apiClient, "archiveTenant")
      .mockRejectedValueOnce(new ApiError(403, "confirm it's you", "reauth_required"))
      .mockResolvedValueOnce(makeProfile());
    vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
      password: { email: "person@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    });
    vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);
    const user = userEvent.setup();
    await renderSignedIn(<ArchiveTenantSection />);
    await openConfirm(user);
    await typeName(user, "Ada's workspace");
    await user.click(screen.getByRole("button", { name: "Archive tenant" }));

    await screen.findByText("Confirm it’s you");
    await user.type(screen.getByLabelText("Current password"), "correct-password");
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await vi.waitFor(() => expect(reload).toHaveBeenCalled());
    expect(archive).toHaveBeenCalledTimes(2);
    expect(archive).toHaveBeenLastCalledWith("t1", "Ada's workspace");
  });

  it("backs out with Cancel without archiving", async () => {
    stub();
    const archive = vi.spyOn(apiClient, "archiveTenant");
    const user = userEvent.setup();
    await renderSignedIn(<ArchiveTenantSection />);
    await openConfirm(user);
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.getByRole("button", { name: /Archive this tenant/ })).toBeInTheDocument();
    expect(archive).not.toHaveBeenCalled();
  });
});
