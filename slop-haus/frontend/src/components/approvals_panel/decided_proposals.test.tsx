import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Proposal } from "@/api";
import { makeMember, makeProposal } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { MemberNamesProvider } from "@/shared/names/member_name";
import { ProposalsProvider } from "@/components/proposals";
import { DEFAULT_APPROVALS_VALUES } from "@/domain/approvals_filter";
import ApprovalsPanel from "./approvals_panel";
import DecidedProposals from "./decided_proposals";

const USER_ID = "33333333-3333-3333-3333-333333333333";

const auto = makeProposal({
  id: "p-auto",
  ticket_id: 7,
  state: "approved",
  outcome: "auto-approved",
  decided_at: "2026-01-02T00:00:00Z",
  decided_by_user_id: USER_ID,
});
const byPerson = makeProposal({
  id: "p-person",
  ticket_id: 8,
  state: "approved",
  outcome: "",
  decided_at: "2026-01-02T00:00:00Z",
  decided_by_user_id: USER_ID,
});
const rejected = makeProposal({ id: "p-rej", ticket_id: 9, state: "rejected", decided_at: "2026-01-02T00:00:00Z", decided_by_user_id: USER_ID });

const FILTER = { status: ["open" as const] };

function view(filter: typeof FILTER | null = FILTER) {
  return renderSignedIn(
    <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
      <DecidedProposals filter={filter} />
    </MemberNamesProvider>,
  );
}

const card = (ticket: number) => within(screen.getByRole("group", { name: `Decided proposal on #${ticket}` }));

describe("DecidedProposals", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({
      members: [makeMember({ id: USER_ID, display_name: "Grace Hopper" })],
      former: [],
    });
    vi.spyOn(apiClient, "listDecidedProposalsPage").mockResolvedValue({ proposals: [auto, byPerson, rejected] });
  });

  it("asks for approved proposals, newest first, with the ticket filter", async () => {
    await view();
    await screen.findAllByRole("group", { name: /^Decided proposal on/ });
    expect(apiClient.listDecidedProposalsPage).toHaveBeenCalledWith(FILTER, "approved", { limit: 50 });
    expect(selectedValueOf("State")).toBe("Approved");
  });

  it("labels an auto-approved card, and not a manually approved one", async () => {
    await view();
    await screen.findAllByRole("group", { name: /^Decided proposal on/ });
    // The selector matters: any other outcome is shown as plain text, so the
    // words alone would be found even without the label.
    expect(card(7).getByText("auto-approved", { selector: ".decided__auto" })).toBeInTheDocument();
    expect(card(8).queryByText("auto-approved")).not.toBeInTheDocument();
  });

  it("shows a rejected card's state", async () => {
    await view();
    await screen.findAllByRole("group", { name: /^Decided proposal on/ });
    expect(card(9).getByText("Rejected")).toBeInTheDocument();
    expect(card(8).getByText("Approved")).toBeInTheDocument();
  });

  it("shows the stale reason, the agent and who decided", async () => {
    vi.mocked(apiClient.listDecidedProposalsPage).mockResolvedValue({
      proposals: [makeProposal({ id: "p-stale", ticket_id: 10, state: "stale", stale_reason: "the ticket was edited", decided_by_user_id: USER_ID })],
    });
    await view();
    await screen.findByRole("group", { name: "Decided proposal on #10" });
    expect(card(10).getByText(/the ticket was edited/)).toBeInTheDocument();
    expect(card(10).getByText(/from console-1/)).toBeInTheDocument();
    expect(await card(10).findByText("Grace Hopper")).toBeInTheDocument();
  });

  it("renders a departed decider as AuthorName does", async () => {
    vi.mocked(apiClient.listDecidedProposalsPage).mockResolvedValue({
      proposals: [{ ...auto, decided_by_user_id: null, decided_by_user_id_deleted: true }],
    });
    await view();
    await screen.findByRole("group", { name: "Decided proposal on #7" });
    expect(card(7).getByText("Deleted user")).toBeInTheDocument();
  });

  it("has no action buttons on a decided card", async () => {
    await view();
    await screen.findAllByRole("group", { name: /^Decided proposal on/ });
    expect(screen.queryByRole("button", { name: /Approve|Reject|Dismiss/ })).not.toBeInTheDocument();
  });

  it("re-queries with the state the picker is changed to", async () => {
    await view();
    await screen.findAllByRole("group", { name: /^Decided proposal on/ });
    await chooseFromDropdown(userEvent.setup(), "State", "Rejected");
    await vi.waitFor(() => expect(apiClient.listDecidedProposalsPage).toHaveBeenLastCalledWith(FILTER, "rejected", { limit: 50 }));
  });

  it("shows only the answer for the state now chosen when an older one lands late", async () => {
    let releaseApproved: (proposals: Proposal[]) => void = () => {};
    vi.mocked(apiClient.listDecidedProposalsPage).mockImplementation((_f, state) =>
      state === "approved"
        ? new Promise((resolve) => (releaseApproved = (proposals) => resolve({ proposals })))
        : Promise.resolve({ proposals: [rejected] }),
    );
    await view();
    await chooseFromDropdown(userEvent.setup(), "State", "Rejected");
    await screen.findByRole("group", { name: "Decided proposal on #9" });
    await act(async () => releaseApproved([auto]));
    expect(screen.queryByRole("group", { name: "Decided proposal on #7" })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Decided proposal on #9" })).toBeInTheDocument();
  });

  it("shows the server's error when the first page fails", async () => {
    vi.mocked(apiClient.listDecidedProposalsPage).mockRejectedValue(new ApiError(500, "database is down"));
    await view();
    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
    expect(screen.queryByRole("group", { name: /^Decided proposal on/ })).not.toBeInTheDocument();
  });

  it("keeps the cards and lets Load more be retried when a later page fails", async () => {
    let fail = true;
    vi.mocked(apiClient.listDecidedProposalsPage).mockImplementation(async (_f, _s, page) => {
      if (!page?.cursor) return { proposals: [auto], nextCursor: "c1" };
      if (fail) throw new ApiError(500, "page failed");
      return { proposals: [rejected] };
    });
    await view();
    await userEvent.click(await screen.findByRole("button", { name: /load more/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("page failed");
    expect(screen.getByRole("group", { name: "Decided proposal on #7" })).toBeInTheDocument();
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: /load more/i }));
    expect(await screen.findByRole("group", { name: "Decided proposal on #9" })).toBeInTheDocument();
  });

  it("pages with the cursor", async () => {
    vi.mocked(apiClient.listDecidedProposalsPage).mockImplementation(async (_f, _s, page) =>
      page?.cursor === "c1" ? { proposals: [rejected] } : { proposals: [auto], nextCursor: "c1" },
    );
    await view();
    await screen.findByRole("group", { name: "Decided proposal on #7" });
    await userEvent.click(await screen.findByRole("button", { name: /load more/i }));
    await screen.findByRole("group", { name: "Decided proposal on #9" });
    expect(apiClient.listDecidedProposalsPage).toHaveBeenLastCalledWith(FILTER, "approved", { limit: 50, cursor: "c1" });
    expect(screen.queryByRole("button", { name: /load more/i })).not.toBeInTheDocument();
  });

  it("says so when there is none, and does not ask when every status is hidden", async () => {
    vi.mocked(apiClient.listDecidedProposalsPage).mockResolvedValue({ proposals: [] });
    await view();
    expect(await screen.findByText("No approved proposals.")).toBeInTheDocument();
  });

  it("makes no request when the filter leaves no status", async () => {
    await view(null);
    expect(await screen.findByText("Every status is hidden, so no tickets match.")).toBeInTheDocument();
    expect(apiClient.listDecidedProposalsPage).not.toHaveBeenCalled();
  });
});

describe("ApprovalsPanel Decided view", () => {
  it("switches between pending and decided, keeping bulk buttons on pending only", async () => {
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });
    const pending = makeProposal({ id: "p-pend", ticket_id: 5 });
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([pending]);
    vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: [pending] });
    vi.spyOn(apiClient, "listDecidedProposalsPage").mockResolvedValue({ proposals: [auto] });
    await renderSignedIn(
      <ProposalsProvider>
        <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
          <ApprovalsPanel values={DEFAULT_APPROVALS_VALUES} onFilterChange={() => {}} />
        </MemberNamesProvider>
      </ProposalsProvider>,
    );
    expect(await screen.findByRole("button", { name: /Reject all/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Decided" }));
    expect(await screen.findByRole("group", { name: "Decided proposal on #7" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Reject all/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Decided" })).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(screen.getByRole("button", { name: "Pending" }));
    expect(await screen.findByRole("button", { name: /Reject all/ })).toBeInTheDocument();
  });
});
