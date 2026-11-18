import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Invite } from "@/api";
import { makeInvite, makeMember } from "@/test/fixtures";
import MembersPanel from "./members_panel";

function setup(overrides: { members?: ReturnType<typeof makeMember>[]; invites?: ReturnType<typeof makeInvite>[] } = {}) {
  vi.spyOn(apiClient, "listMembers").mockResolvedValue(overrides.members ?? [makeMember()]);
  vi.spyOn(apiClient, "listInvites").mockResolvedValue(overrides.invites ?? []);
}

describe("MembersPanel", () => {
  it("lists the tenant's members", async () => {
    setup({ members: [makeMember({ display_name: "Ada Lovelace" }), makeMember({ id: "u2", display_name: "Bea" })] });
    render(<MembersPanel />);

    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(screen.getByText("Bea")).toBeInTheDocument();
  });

  it("shows the error message when members fail to load", async () => {
    vi.spyOn(apiClient, "listMembers").mockRejectedValue(new ApiError(500, "boom"));
    vi.spyOn(apiClient, "listInvites").mockResolvedValue([]);
    render(<MembersPanel />);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
  });

  it("lists outstanding invites and says so when there are none", async () => {
    setup({ invites: [] });
    render(<MembersPanel />);

    expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
  });

  it("shows an outstanding invite by email", async () => {
    setup({ invites: [makeInvite({ email: "invitee@example.com" })] });
    render(<MembersPanel />);

    expect(await screen.findByText("invitee@example.com")).toBeInTheDocument();
  });

  it("sends an invite and refreshes the list", async () => {
    setup({ invites: [] });
    vi.spyOn(apiClient, "createInvite").mockResolvedValue(makeInvite({ email: "new@example.com" }));
    const user = userEvent.setup();
    render(<MembersPanel />);
    await screen.findByText("No outstanding invites.");

    vi.spyOn(apiClient, "listInvites").mockResolvedValue([makeInvite({ email: "new@example.com" })]);
    await user.type(screen.getByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));

    expect(apiClient.createInvite).toHaveBeenCalledWith("new@example.com");
    expect(await screen.findByText("new@example.com")).toBeInTheDocument();
  });

  it("shows the server's error when sending an invite fails", async () => {
    setup({ invites: [] });
    vi.spyOn(apiClient, "createInvite").mockRejectedValue(new ApiError(409, "already a member of this tenant"));
    const user = userEvent.setup();
    render(<MembersPanel />);

    await user.type(await screen.findByLabelText("Email"), "existing@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("already a member of this tenant");
  });

  it("revokes an invite and refreshes the list", async () => {
    setup({ invites: [makeInvite({ id: "i1", email: "invitee@example.com" })] });
    vi.spyOn(apiClient, "revokeInvite").mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<MembersPanel />);
    await screen.findByText("invitee@example.com");

    vi.spyOn(apiClient, "listInvites").mockResolvedValue([]);
    await user.click(screen.getByRole("button", { name: "Revoke" }));

    expect(apiClient.revokeInvite).toHaveBeenCalledWith("i1");
    expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
  });

  // The mount fetch and a post-invite refresh can be in flight together;
  // without the issued/applied guard in members_panel.tsx, whichever
  // happens to resolve *last* wins, even if it's the older one. Negative-run
  // by dropping that guard: this test fails (the invite disappears again)
  // where the others above -- which never let two requests overlap --
  // would not have caught it.
  it("keeps the newer refresh even when an older, slower one resolves after it", async () => {
    vi.spyOn(apiClient, "listMembers").mockResolvedValue([makeMember()]);
    let resolveMountFetch: (invites: Invite[]) => void = () => {};
    const mountFetch = new Promise<Invite[]>((resolve) => {
      resolveMountFetch = resolve;
    });
    const listInvites = vi.spyOn(apiClient, "listInvites").mockReturnValueOnce(mountFetch);
    render(<MembersPanel />);
    await screen.findByText("Loading invites…");

    vi.spyOn(apiClient, "createInvite").mockResolvedValue(makeInvite({ email: "new@example.com" }));
    listInvites.mockResolvedValueOnce([makeInvite({ email: "new@example.com" })]);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Send invite" }));
    await screen.findByText("new@example.com");

    // The mount fetch -- issued before the post-invite refresh -- finally
    // resolves with stale (empty) data. It must not erase the newer list.
    resolveMountFetch([]);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByText("new@example.com")).toBeInTheDocument();
  });
});
