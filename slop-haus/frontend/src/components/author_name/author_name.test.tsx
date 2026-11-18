import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeAgent, makeMember } from "@/test/fixtures";
import { AgentNamesProvider } from "@/shared/names/agent_name";
import { MemberNamesProvider } from "@/shared/names/member_name";
import AuthorName from "./author_name";

const AGENT_ID = "11111111-1111-1111-1111-111111111111";
const USER_ID = "22222222-2222-2222-2222-222222222222";

function renderWithProviders(agentId: string | null, userId: string | null, userDeleted = false) {
  return render(
    <AgentNamesProvider>
      <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
        <AuthorName agentId={agentId} userId={userId} userDeleted={userDeleted} />
      </MemberNamesProvider>
    </AgentNamesProvider>,
  );
}

describe("AuthorName", () => {
  it("renders an agent's name when an agent id is set", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT_ID, name: "builder-1" })]);

    renderWithProviders(AGENT_ID, null);

    expect(await screen.findByText("builder-1")).toBeInTheDocument();
  });

  it("renders a member's name when a user id is set and no agent id is", async () => {
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({
      members: [makeMember({ id: USER_ID, display_name: "Grace Hopper" })],
      former: [],
    });

    renderWithProviders(null, USER_ID);

    expect(await screen.findByText("Grace Hopper")).toBeInTheDocument();
  });

  // Neither id set used to mean "the human" -- it means nobody, a notice
  // from the server itself, and must not be rendered as a person.
  it("renders a distinct notice when neither id is set", () => {
    renderWithProviders(null, null);

    expect(screen.getByText("System")).toBeInTheDocument();
    expect(screen.queryByText("someone")).not.toBeInTheDocument();
  });

  // The server sends no id for a deleted account, only the flag: it is not
  // "System" (no person at all) nor "someone" (an id nobody can name).
  it("renders 'Deleted user' for a deleted account, distinct from System and someone", () => {
    const { container } = renderWithProviders(null, null, true);

    expect(screen.getByText("Deleted user")).toBeInTheDocument();
    expect(screen.queryByText("System")).not.toBeInTheDocument();
    expect(screen.queryByText("someone")).not.toBeInTheDocument();
    expect(container.querySelectorAll("[title]")).toHaveLength(0);
  });

  it("renders 'someone' for an id it cannot name, and '(former member)' for one who left", async () => {
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({
      members: [],
      former: [makeMember({ id: USER_ID, display_name: "Grace Hopper" })],
    });

    const { container } = render(
      <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
        <AuthorName agentId={null} userId={USER_ID} />
        <AuthorName agentId={null} userId="33333333-3333-3333-3333-333333333333" />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("Grace Hopper (former member)")).toBeInTheDocument();
    expect(screen.getByText("someone")).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(USER_ID);
  });

  it("prefers an agent over the deleted flag", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT_ID, name: "builder-1" })]);

    renderWithProviders(AGENT_ID, null, true);

    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    expect(screen.queryByText("Deleted user")).not.toBeInTheDocument();
  });
});
