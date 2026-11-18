import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeProposal } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { ProposalsProvider } from "@/components/proposals";
import ProposalsStrip from "./proposals_strip";

describe("ProposalsStrip", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
  });

  it("says what is proposed for this ticket, in the server's words, and nothing for others", async () => {
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([
      makeProposal({ id: "a", ticket_id: 7, agent_name: "console-1", action_line: "Move #7 into the queue." }),
      makeProposal({ id: "b", ticket_id: 8, action_line: "Move #8 into the queue." }),
    ]);
    await renderSignedIn(
      <ProposalsProvider>
        <ProposalsStrip ticketId={7} />
      </ProposalsProvider>,
    );

    expect(await screen.findByText("Move #7 into the queue.", { exact: false })).toBeInTheDocument();
    expect(screen.getByText(/Proposed by console-1/)).toBeInTheDocument();
    expect(screen.queryByText(/#8/)).toBeNull();
    expect(screen.getByRole("link", { name: "Review on the Approvals tab" })).toHaveAttribute("href", "/approvals");
  });

  it("renders nothing when the ticket has no proposal", async () => {
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([makeProposal({ ticket_id: 8 })]);
    const { container } = await renderSignedIn(
      <ProposalsProvider>
        <ProposalsStrip ticketId={7} />
      </ProposalsProvider>,
    );
    await vi.waitFor(() => expect(apiClient.listProposals).toHaveBeenCalled());
    expect(container.querySelector(".proposals-strip")).toBeNull();
  });
});
