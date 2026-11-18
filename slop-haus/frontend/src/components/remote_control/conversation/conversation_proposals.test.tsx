import { beforeEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Proposal } from "@/api";
import { makeProposal } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { ProposalsContext } from "@/components/proposals";
import ConversationProposals from "./conversation_proposals";

const reload = vi.fn(async () => {});

async function show(proposals: Proposal[]) {
  await renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals, reload }}>
      <ConversationProposals proposals={proposals} />
    </ProposalsContext.Provider>,
  );
}

const expand = async (action_line: string) => userEvent.click(await screen.findByRole("button", { name: `Show details of: ${action_line}` }));

describe("ConversationProposals deciding in place", () => {
  beforeEach(() => {
    reload.mockClear();
  });

  it("offers Approve and Reject only once the proposal is expanded", async () => {
    const p = makeProposal({ id: "p1", action_line: "Answer it." });
    await show([p]);

    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
    await expand("Answer it.");
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Hide details of: Answer it." }));
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("approves through the same endpoint and refreshes the proposals", async () => {
    const approve = vi.spyOn(apiClient, "approveProposal").mockResolvedValue(undefined as never);
    await show([makeProposal({ id: "p1", action_line: "Answer it." })]);
    await expand("Answer it.");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    expect(approve).toHaveBeenCalledWith("p1");
    await vi.waitFor(() => expect(reload).toHaveBeenCalled());
  });

  it("reports a refused approval even after the settled proposal has left the band", async () => {
    vi.spyOn(apiClient, "approveProposal").mockRejectedValue(new ApiError(409, "the ticket has changed since it was proposed"));
    const p = makeProposal({ id: "p1", action_line: "Answer it." });
    // The server has settled the proposal by the time it refuses, so the
    // refresh removes it from the band.
    function Harness() {
      const [pending, setPending] = useState([p]);
      return (
        <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: pending, reload: async () => setPending([]) }}>
          <ConversationProposals proposals={pending} />
        </ProposalsContext.Provider>
      );
    }
    await renderSignedIn(<Harness />);
    await expand("Answer it.");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByRole("status")).toHaveTextContent(/Not applied to #7: the ticket has changed/);
  });

  it("rejects with the reason", async () => {
    const rej = vi.spyOn(apiClient, "rejectProposal").mockResolvedValue(undefined as never);
    await show([makeProposal({ id: "p1", action_line: "Answer it." })]);
    await expand("Answer it.");

    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    await userEvent.type(screen.getByRole("textbox"), "not now");
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));

    expect(rej).toHaveBeenCalledWith("p1", "not now");
  });

  it("cannot approve a stale proposal", async () => {
    await show([makeProposal({ id: "p1", action_line: "Answer it.", stale_reason: "the ticket was edited" })]);
    await expand("Answer it.");

    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.getByRole("note")).toHaveTextContent(/has changed since/);
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeInTheDocument();
  });

  it.each([
    ["cancelling a ticket", { kind: "status", status: "cancelled", text: "obsolete" }],
    ["approving a review", { kind: "review_decision", decision: "approve", text: "" }],
  ] as const)("asks to confirm %s before approving", async (_name, over) => {
    const approve = vi.spyOn(apiClient, "approveProposal").mockResolvedValue(undefined as never);
    await show([makeProposal({ id: "p1", action_line: "Irreversible.", ...over })]);
    await expand("Irreversible.");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(approve).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: /^Confirm:/ }));
    expect(approve).toHaveBeenCalledWith("p1");
  });

  it("approves other kinds without a confirmation", async () => {
    const approve = vi.spyOn(apiClient, "approveProposal").mockResolvedValue(undefined as never);
    await show([makeProposal({ id: "p1", action_line: "Park it.", kind: "status", status: "parked", text: "" })]);
    await expand("Park it.");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(approve).toHaveBeenCalledWith("p1");
  });
});
