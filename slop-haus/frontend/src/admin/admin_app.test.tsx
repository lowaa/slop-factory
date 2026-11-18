import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Invite } from "@/api";
import { makeInvite, makeProfile } from "@/test/fixtures";
import AdminApp from "./admin_app";

// AdminApp wraps a real AuthProvider (see its own doc comment on why
// that's safe to share with the main app), so these tests drive it through
// apiClient.me() the same way app.test.tsx does,
// rather than mocking a context.

function signedOut() {
  vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
}

function signedInAsAdmin() {
  vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ is_admin: true, display_name: "Ada Admin" }));
}

describe("AdminApp", () => {
  it("offers to sign in when signed out", async () => {
    signedOut();
    render(<AdminApp />);

    expect(await screen.findByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });

  it("lists outstanding admin invites once signed in", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([makeInvite({ email: "invitee@example.com", kind: "new_tenant" })]);
    render(<AdminApp />);

    expect(await screen.findByText("invitee@example.com")).toBeInTheDocument();
    expect(await screen.findByText("Ada Admin")).toBeInTheDocument();
  });

  it("says so when there are no outstanding invites", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([]);
    render(<AdminApp />);

    expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
  });

  it("sends an admin invite and refreshes the list", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([]);
    vi.spyOn(apiClient, "createAdminInvite").mockResolvedValue(makeInvite({ email: "new@example.com", kind: "new_tenant" }));
    const user = userEvent.setup();
    render(<AdminApp />);
    await screen.findByText("No outstanding invites.");

    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([makeInvite({ email: "new@example.com", kind: "new_tenant" })]);
    await user.type(screen.getByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));

    expect(apiClient.createAdminInvite).toHaveBeenCalledWith("new@example.com");
    expect(await screen.findByText("new@example.com")).toBeInTheDocument();
  });

  it("shows the server's error when sending an admin invite fails", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([]);
    vi.spyOn(apiClient, "createAdminInvite").mockRejectedValue(new ApiError(429, "too many attempts, try again later"));
    const user = userEvent.setup();
    render(<AdminApp />);

    await user.type(await screen.findByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/too many attempts/i);
  });

  it("revokes an admin invite and refreshes the list", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([makeInvite({ id: "i1", email: "invitee@example.com", kind: "new_tenant" })]);
    vi.spyOn(apiClient, "revokeAdminInvite").mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<AdminApp />);
    await screen.findByText("invitee@example.com");

    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([]);
    await user.click(screen.getByRole("button", { name: "Revoke" }));

    expect(apiClient.revokeAdminInvite).toHaveBeenCalledWith("i1");
    expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
  });

  it("shows the server's error when revoking an admin invite fails", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([makeInvite({ id: "i1", email: "invitee@example.com" })]);
    vi.spyOn(apiClient, "revokeAdminInvite").mockRejectedValue(new ApiError(404, "not found"));
    const user = userEvent.setup();
    render(<AdminApp />);

    await user.click(await screen.findByRole("button", { name: "Revoke" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not found");
  });

  // Same race as members_panel.test.tsx's identical test -- AdminInvites
  // carries the same issued/applied guard for the same reason.
  it("keeps the newer refresh even when an older, slower one resolves after it", async () => {
    signedInAsAdmin();
    let resolveMountFetch: (invites: Invite[]) => void = () => {};
    const mountFetch = new Promise<Invite[]>((resolve) => {
      resolveMountFetch = resolve;
    });
    const listAdminInvites = vi.spyOn(apiClient, "listAdminInvites").mockReturnValueOnce(mountFetch);
    render(<AdminApp />);
    await screen.findByText("Loading invites…");

    vi.spyOn(apiClient, "createAdminInvite").mockResolvedValue(makeInvite({ email: "new@example.com" }));
    listAdminInvites.mockResolvedValueOnce([makeInvite({ email: "new@example.com" })]);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));
    await screen.findByText("new@example.com");

    resolveMountFetch([]);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByText("new@example.com")).toBeInTheDocument();
  });

  it("signs out via the header button", async () => {
    signedInAsAdmin();
    vi.spyOn(apiClient, "listAdminInvites").mockResolvedValue([]);
    vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<AdminApp />);

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(apiClient.logout).toHaveBeenCalled();
    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
  });
});
