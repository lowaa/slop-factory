import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { apiClient, type BoardChange } from "@/api";
import { makeProposal } from "@/test/fixtures";
import { ProposalsProvider } from "./proposals_provider";
import { useProposals } from "./proposals";

function Ids() {
  const { status, proposals } = useProposals();
  return <p>{`${status}:${proposals.map((p) => p.id).join(",")}`}</p>;
}

describe("ProposalsProvider", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("lists the pending proposals oldest first, and refetches when the board's stream reports a change", async () => {
    // The server lists newest first.
    const list = vi.spyOn(apiClient, "listProposals").mockResolvedValue([makeProposal({ id: "p-2" }), makeProposal({ id: "p-1" })]);
    let send: (change: BoardChange) => void = () => {};
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange) => {
      send = (change) => act(() => onChange(change.revision, change));
      return () => {};
    });

    render(
      <ProposalsProvider>
        <Ids />
      </ProposalsProvider>,
    );
    expect(await screen.findByText("loaded:p-1,p-2")).toBeInTheDocument();

    list.mockResolvedValue([makeProposal({ id: "p-3" }), makeProposal({ id: "p-2" }), makeProposal({ id: "p-1" })]);
    // The first event after mount waits out the throttle window.
    await waitFor(
      () => {
        send({ revision: 2, reload: true });
        expect(screen.getByText("loaded:p-1,p-2,p-3")).toBeInTheDocument();
      },
      { timeout: 5000 },
    );
  });

  it("reports an error when the first fetch fails", async () => {
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
    vi.spyOn(apiClient, "listProposals").mockRejectedValue(new Error("down"));
    render(
      <ProposalsProvider>
        <Ids />
      </ProposalsProvider>,
    );
    expect(await screen.findByText("error:")).toBeInTheDocument();
  });
});
