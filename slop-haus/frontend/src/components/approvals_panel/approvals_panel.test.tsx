import { beforeEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Proposal } from "@/api";
import { makeEditProposal, makeManifestProposal, makeProposal } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { makeRunningAttempt } from "@/test/running_attempt";
import { makeTicket } from "@/test/fixtures";
import { ProposalsProvider } from "@/components/proposals";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import { DEFAULT_APPROVALS_VALUES } from "@/domain/approvals_filter";
import ApprovalsPanel from "./approvals_panel";
import { chooseFromDropdown } from "@/test/widgets";

// Holds the filter as the route would, so a change made in the page comes back.
function Filtered({ initial }: { initial: FilterValues }) {
  const [values, setValues] = useState(initial);
  return <ApprovalsPanel values={values} onFilterChange={setValues} />;
}

function panel(initial: FilterValues = DEFAULT_APPROVALS_VALUES) {
  return renderSignedIn(
    <ProposalsProvider>
      <Filtered initial={initial} />
    </ProposalsProvider>,
  );
}

// serverList is the proposals the server says are pending, newest first as
// the shared list gets them; the panel's own pages come oldest first. A test
// changes it to say what the next fetch returns.
function serverList(initial: Proposal[]) {
  let current = initial;
  vi.spyOn(apiClient, "listProposals").mockImplementation(async () => current);
  vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async () => ({ proposals: [...current].reverse() }));
  return (next: Proposal[]) => {
    current = next;
  };
}

const cardsShown = () => screen.queryAllByRole("group", { name: /^Proposal on/ });

const answer = makeProposal({ id: "p-answer", ticket_id: 7, kind: "discussion_answer" });
const ready = makeProposal({
  id: "p-ready",
  ticket_id: 8,
  ticket_title: "Plan the thing",
  kind: "ready",
  ticket_status: "preparing",
  text: "",
  action_line: "Move #8 out of preparing and into the queue, so an agent can pick it up.",
});

describe("ApprovalsPanel", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
  });

  it("uses the shared button variants: approve is primary, the reject confirmation is danger", async () => {
    serverList([ready]);
    await panel();
    const card = within(await screen.findByRole("group", { name: /^Proposal on/ }));
    expect(card.getByRole("button", { name: "Approve" })).toHaveClass("button--primary");
    const reject = card.getByRole("button", { name: "Reject" });
    expect(reject).toHaveClass("button--secondary");

    await userEvent.click(reject);
    expect(card.getByRole("button", { name: "Reject" })).toHaveClass("button--danger");
    expect(card.getByRole("button", { name: "Cancel" })).toHaveClass("button--secondary");
  });

  it("shows a proposal without a ticket as the workspace's, with no ticket link", async () => {
    const workspace = makeProposal({
      id: "p-workspace",
      ticket_id: null,
      ticket_title: "",
      subject: "tenant",
      approver_role: "owner",
      text: "",
      action_line: "Apply the change below to the workspace.",
    });
    serverList([workspace]);
    await panel();
    const card = within(await screen.findByRole("group", { name: "Proposal on the workspace" }));
    expect(card.getByText("Workspace")).toBeInTheDocument();
    expect(card.queryByRole("link")).not.toBeInTheDocument();
    expect(card.getByText("Apply the change below to the workspace.")).toBeInTheDocument();
    expect(card.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("shows a manifest proposal as the rules it adds and removes, not raw JSON", async () => {
    serverList([makeManifestProposal()]);
    await panel();
    const card = within(await screen.findByRole("group", { name: "Proposal on the workspace" }));
    const changes = within(card.getByRole("region", { name: "Changes to the workspace manifest" }));
    expect(changes.getByText("+ deploy/*/kustomization.yaml (lines matching \\s*newTag: \\S+)")).toBeInTheDocument();
    expect(changes.getByText("+ db/migrations/ (migration)")).toBeInTheDocument();
    expect(changes.getByText("~ gen/: resolved by migration becomes regenerate")).toBeInTheDocument();
    expect(changes.getByText("+ slop-haus/")).toBeInTheDocument();
    expect(changes.getByText("- billing/")).toBeInTheDocument();
    expect(changes.getByText("+ infrastructure/ (prod)")).toBeInTheDocument();
    expect(changes.getByText("- old/")).toBeInTheDocument();
    expect(changes.getByText("Batch merges within an app: off becomes on")).toBeInTheDocument();
    expect(changes.getByText("Skip re-testing when main moved only in other apps: off becomes on")).toBeInTheDocument();
    expect(changes.getByText("removed")).toBeInTheDocument();
    expect(changes.getByText("Deploy-only (loosens the merge gate)")).toBeInTheDocument();
    expect(card.queryByText(/payload_version|base_revision/)).not.toBeInTheDocument();
  });

  it("a manifest proposal the manifest has moved on from can only be dismissed, and says so", async () => {
    serverList([makeManifestProposal({ stale_reason: "it was revision 3, it is 4" })]);
    await panel();
    const card = within(await screen.findByRole("group", { name: "Proposal on the workspace" }));
    expect(card.getByRole("note")).toHaveTextContent(
      "The workspace manifest has changed since this was proposed (it was revision 3, it is 4)",
    );
    expect(card.queryByRole("button", { name: /^Approve/ })).not.toBeInTheDocument();
    expect(card.getByRole("button", { name: "Dismiss" })).toBeEnabled();
  });

  it("warns on a close-as-merged proposal what approving accepts, and says when it would be refused", async () => {
    const closeMerged = (close_merged: NonNullable<Proposal["close_merged"]>) =>
      makeProposal({
        kind: "close_merged",
        approver_role: "owner",
        ticket_status: "open",
        text: "merged by hand",
        action_line: "Close #7 (now open) as done, because its change was merged outside slop-haus.",
        close_merged,
      });
    serverList([closeMerged({ confirmations: ["the checks on the merged head abc did not pass (failed)"] })]);
    await panel();
    const card = within(await screen.findByRole("group", { name: /Proposal on/ }));
    expect(card.getByRole("note")).toHaveTextContent("Approving closes it anyway, though:");
    expect(card.getByRole("note")).toHaveTextContent("did not pass (failed)");
    expect(card.getByText("merged by hand")).toBeInTheDocument();
    expect(card.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("shows why a close-as-merged proposal would be refused now", async () => {
    serverList([
      makeProposal({
        kind: "close_merged",
        approver_role: "owner",
        text: "merged by hand",
        close_merged: { confirmations: [], problem: "the code host does not show the change merged into main" },
      }),
    ]);
    await panel();
    const card = within(await screen.findByRole("group", { name: /Proposal on/ }));
    expect(card.getByRole("note")).toHaveTextContent("Approving would be refused now: the code host does not show the change merged into main");
  });

  it("lists what a decided manifest proposal proposed, with nothing to compare it to", async () => {
    const pending = makeManifestProposal();
    serverList([makeManifestProposal({ manifest_edit: { ...pending.manifest_edit!, changes: undefined } })]);
    await panel();
    const card = within(await screen.findByRole("group", { name: "Proposal on the workspace" }));
    expect(card.getByText(/The manifest proposed/)).toBeInTheDocument();
    expect(card.getByText("Remote: github.com/acme/app")).toBeInTheDocument();
    expect(card.getByText("+ independent app slop-haus/")).toBeInTheDocument();
    expect(card.getByText("+ protected infrastructure/ (prod)")).toBeInTheDocument();
    expect(card.getByText("Batch merges within an app: on")).toBeInTheDocument();
    expect(card.getByText("Skip re-testing when main moved only in other apps: on")).toBeInTheDocument();
  });

  it("follows the board's stream: a change refetches and the list updates", async () => {
    const setServer = serverList([answer]);
    let onChange: (revision: number) => void = () => {};
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((cb) => {
      onChange = (revision) => cb(revision, { revision, reload: true });
      return () => {};
    });
    await panel();
    expect(await screen.findAllByRole("group", { name: /^Proposal on/ })).toHaveLength(1);

    setServer([ready, answer]);
    // The first stream event after mount is the revision as it stands; the
    // throttle's window has to pass before the refetch it asks for.
    await vi.waitFor(() => {
      onChange(2);
      expect(screen.getAllByRole("group", { name: /^Proposal on/ })).toHaveLength(2);
    }, { timeout: 5000 });
  });

  it("does not let a slow older fetch put back a proposal a newer one dropped", async () => {
    let releaseOld: (list: Proposal[]) => void = () => {};
    const calls: ("old" | "new")[] = [];
    vi.spyOn(apiClient, "listProposals").mockImplementation(async () => []);
    vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(() => {
      calls.push(calls.length === 0 ? "old" : "new");
      if (calls.length === 1) return new Promise((resolve) => (releaseOld = (list) => resolve({ proposals: list })));
      return Promise.resolve({ proposals: [] });
    });
    let onChange: (revision: number) => void = () => {};
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((cb) => {
      onChange = (revision) => cb(revision, { revision, reload: true });
      return () => {};
    });
    await panel();
    await vi.waitFor(() => {
      onChange(2);
      expect(calls).toContain("new");
    }, { timeout: 5000 });
    await screen.findByText("Nothing waiting for you.");

    await act(async () => {
      releaseOld([answer]);
    });
    expect(cardsShown()).toHaveLength(0);
  });

  it("lists what is waiting, oldest first, with the server's action line and who proposed it", async () => {
    serverList([ready, answer]);
    await panel();

    const cards = await screen.findAllByRole("group", { name: /^Proposal on/ });
    expect(cards.map((c) => c.getAttribute("aria-label"))).toEqual(["Proposal on #7", "Proposal on #8"]);
    expect(within(cards[0]).getByText(/Post the text below as your comment on #7/)).toBeInTheDocument();
    expect(within(cards[0]).getByText(/from console-1/)).toBeInTheDocument();
    expect(within(cards[0]).getAllByRole("link")[0].getAttribute("href")).toContain("from=approvals");
    expect(apiClient.listPendingProposalsPage).toHaveBeenCalledWith(expect.objectContaining({ status: expect.any(Array) }), { limit: 50 });
  });

  it("asks the list to fill the window the layout leaves", async () => {
    serverList([ready]);
    await panel();
    await screen.findByRole("group", { name: /^Proposal on/ });
    expect(document.querySelector(".approvals .virtual-list")).toHaveClass("virtual-list--fill");
  });

  it("says so when nothing is waiting", async () => {
    serverList([]);
    await panel();
    expect(await screen.findByText("Nothing waiting for you.")).toBeInTheDocument();
  });

  it("shows the server's error when the list fails to load", async () => {
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
    vi.spyOn(apiClient, "listPendingProposalsPage").mockRejectedValue(new ApiError(500, "database is down"));
    await panel();
    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
  });

  // The person decides on what they see: the agent's text must be the bytes
  // that will be posted, never rendered into markup.
  it("shows the agent's text as plain text, never as markup", async () => {
    const hostile = '<script>alert(1)</script> **bold** <img src=x onerror=alert(1)>\n[a link](https://evil.example)';
    serverList([makeProposal({ text: hostile })]);
    const { container } = await panel();

    const body = await screen.findByText((_, el) => el?.tagName === "PRE" && el.textContent === hostile);
    expect(body).toBeInTheDocument();
    expect(container.querySelector("script, img, strong")).toBeNull();
    expect(screen.queryByRole("link", { name: "a link" })).toBeNull();
  });

  it("folds long text behind Show all", async () => {
    const long = "x".repeat(900);
    serverList([makeProposal({ text: long })]);
    await panel();

    const toggle = await screen.findByRole("button", { name: "Show all" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(toggle);
    expect(screen.getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
  });

  it("shows an edit as what it changes: a diff of the description, paths added and removed, priority and overlaps", async () => {
    serverList([makeEditProposal()]);
    await panel();

    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.getByText("Retire the importer")).toBeInTheDocument();
    expect(changes.getByText("Retire the old importer")).toBeInTheDocument();
    expect(changes.getByText("- Keep the exporter.")).toBeInTheDocument();
    expect(changes.getByText("+ Keep the exporter and its tests.")).toBeInTheDocument();
    expect(changes.getByText("+ docs/")).toBeInTheDocument();
    expect(changes.getByText("- legacy/")).toBeInTheDocument();
    expect(changes.queryByText(/importer\//)).toBeNull();
    expect(changes.getByText("normal")).toBeInTheDocument();
    expect(changes.getByText("high")).toBeInTheDocument();
    expect(changes.getByRole("note")).toHaveTextContent("#30 Docs overhaul (in_progress): docs/ overlaps docs/");
  });

  it("marks an overlap inside a shared path on an edit's card", async () => {
    const proposal = makeEditProposal();
    proposal.edit!.overlaps = [
      { ticket_id: 31, title: "Migrator", status: "in_progress", path: "db/", other: "db/m/", resolve: "migration" },
    ];
    serverList([proposal]);
    await panel();

    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.getByRole("note")).toHaveTextContent("shared path, blocks nothing; on a clash: migration");
  });

  it("says an overlap blocks nothing on an edit's card when the tenant has scope locking off", async () => {
    const proposal = makeEditProposal();
    proposal.edit!.overlaps = [
      { ticket_id: 31, title: "Real", status: "in_progress", path: "db/", other: "db/x.go", locking_off: true },
    ];
    serverList([proposal]);
    await panel();

    const note = within(await screen.findByRole("region", { name: /^Changes to #/ })).getByRole("note");
    expect(note).toHaveTextContent("scope locking is off for this tenant");
    expect(note).toHaveTextContent("scope locking is off, blocks nothing");
    expect(note).not.toHaveTextContent("can't be claimed in parallel");
  });

  it("says an edit that opens the ticket also moves it to open", async () => {
    serverList([makeEditProposal({ edit: { ...makeEditProposal().edit, open: true } })]);
    await panel();
    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.getByText("and moves it to open")).toBeInTheDocument();
  });

  it("does not mention opening for an edit that leaves the status alone", async () => {
    serverList([makeEditProposal()]);
    await panel();
    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.queryByText(/moves it to open/)).toBeNull();
  });

  it("shows only the fields an edit changes, and the new text alone once decided", async () => {
    serverList([
      makeEditProposal({ edit: { body: { new: "Just the new text, markup <b>stays text</b>" }, paths: ["a/"] } }),
    ]);
    const { container } = await panel();

    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.queryByText("Title")).toBeNull();
    expect(changes.queryByText("Priority")).toBeNull();
    expect(changes.getByText("Just the new text, markup <b>stays text</b>")).toBeInTheDocument();
    expect(container.querySelector("b")).toBeNull();
    expect(changes.getByText("a/")).toBeInTheDocument();
  });

  it("says when the proposed paths are the ticket's already, and never shows a decided edit's old text", async () => {
    serverList([
      makeEditProposal({ edit: { paths: ["a/"], paths_added: [], paths_removed: [], body: { new: "New text" } } }),
    ]);
    await panel();

    const changes = within(await screen.findByRole("region", { name: /^Changes to #/ }));
    expect(changes.getByText("Unchanged from the ticket now.")).toBeInTheDocument();
    expect(changes.queryByText(/Remove the importer/)).toBeNull();
  });

  it("approves one proposal and refetches the list", async () => {
    const setServer = serverList([answer]);
    vi.spyOn(apiClient, "approveProposal").mockImplementation(async () => {
      setServer([]);
      return { ...answer, state: "approved" };
    });
    await panel();

    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));

    expect(apiClient.approveProposal).toHaveBeenCalledWith("p-answer");
    expect(await screen.findByText("Nothing waiting for you.")).toBeInTheDocument();
  });

  it("rejects with a reason the console will read back", async () => {
    const setServer = serverList([answer]);
    vi.spyOn(apiClient, "rejectProposal").mockImplementation(async () => {
      setServer([]);
      return { ...answer, state: "rejected" };
    });
    await panel();

    await userEvent.click(await screen.findByRole("button", { name: "Reject" }));
    await userEvent.type(screen.getByLabelText(/Reason/), "ask me first");
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));

    expect(apiClient.rejectProposal).toHaveBeenCalledWith("p-answer", "ask me first");
    expect(await screen.findByText("Nothing waiting for you.")).toBeInTheDocument();
  });

  it("offers only Dismiss for a proposal whose ticket has moved on", async () => {
    const stale = makeProposal({ stale_reason: "its title or text was edited" });
    serverList([stale]);
    vi.spyOn(apiClient, "rejectProposal").mockResolvedValue({ ...stale, state: "rejected" });
    await panel();

    expect(await screen.findByRole("note")).toHaveTextContent("its title or text was edited");
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(apiClient.rejectProposal).toHaveBeenCalledWith(stale.id, "the ticket changed since it was proposed");
  });

  it("reports a refused approval and refetches, since the server settled it", async () => {
    serverList([answer]);
    vi.spyOn(apiClient, "approveProposal").mockRejectedValue(
      new ApiError(409, "the ticket has changed since it was proposed: its title or text was edited"),
    );
    await panel();
    const filteredFetches = () => vi.mocked(apiClient.listPendingProposalsPage).mock.calls.length;
    const approve = await screen.findByRole("button", { name: "Approve" });
    const before = filteredFetches();

    await userEvent.click(approve);

    expect(await screen.findByText(/Not applied to #7: the ticket has changed/)).toBeInTheDocument();
    await vi.waitFor(() => expect(filteredFetches()).toBeGreaterThan(before));
  });

  it("approves all of one kind, never review decisions or cancellations, and reports each result", async () => {
    const cancel = makeProposal({ id: "p-cancel", ticket_id: 9, kind: "status", status: "cancelled", text: "obsolete" });
    const review = makeProposal({ id: "p-review", ticket_id: 10, kind: "review_decision", decision: "approve", text: "" });
    const stale = makeProposal({ id: "p-stale", ticket_id: 11, kind: "ready", stale_reason: "its PR changed" });
    const setServer = serverList([cancel, review, stale, ready, answer]);
    vi.spyOn(apiClient, "approveAllProposals").mockImplementation(async () => {
      setServer([cancel, review, stale]);
      return [{ id: "p-ready", state: "approved", outcome: "", error: "" }];
    });
    await panel();

    // One ready is approvable (the stale one is not counted), one answer.
    expect(await screen.findByRole("button", { name: "Approve all to open (1)" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve all answers (1)" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Approve all.*(status|review|cancel)/i })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Approve all to open (1)" }));

    expect(apiClient.approveAllProposals).toHaveBeenCalledWith("ready", ["p-ready"]);
    expect(await screen.findByText(/#8: approved/)).toBeInTheDocument();
  });

  it("rejects everything at once with one reason", async () => {
    const setServer = serverList([ready, answer]);
    vi.spyOn(apiClient, "rejectAllProposals").mockImplementation(async () => {
      setServer([]);
      return [];
    });
    await panel();

    await userEvent.click(await screen.findByRole("button", { name: "Reject all (2)" }));
    await userEvent.type(screen.getByLabelText(/Reason/), "starting over");
    await userEvent.click(screen.getByRole("button", { name: "Reject all 2" }));

    expect(apiClient.rejectAllProposals).toHaveBeenCalledWith("starting over", ["p-answer", "p-ready"]);
    expect(await screen.findByText("Nothing waiting for you.")).toBeInTheDocument();
  });

  describe("paging and windowing", () => {
    const many = (n: number) =>
      Array.from({ length: n }, (_, i) => makeProposal({ id: `p-${i}`, ticket_id: 100 + i, kind: "ready", text: "" }));

    it("draws only the cards near the window, not every proposal it was sent", async () => {
      const page = many(50);
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: page });
      await panel();

      await screen.findAllByRole("group", { name: /^Proposal on/ });
      const drawn = cardsShown().length;
      expect(drawn).toBeGreaterThan(0);
      expect(drawn).toBeLessThan(page.length);
    });

    // The panel fetches again when the shared list first lands, so a test
    // answers by cursor rather than by call order.
    const byCursor = (pages: Record<string, { proposals: Proposal[]; nextCursor?: string }>) =>
      vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async (_filter, page) => pages[page?.cursor ?? ""]);

    it("asks for the next page with the cursor, appends it, and stops offering more at the end", async () => {
      const [a, b, c] = many(3);
      const next = byCursor({ "": { proposals: [a, b], nextCursor: "cur-1" }, "cur-1": { proposals: [c] } });
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      await panel();

      await userEvent.click(await screen.findByRole("button", { name: "Load more" }));

      await vi.waitFor(() => expect(cardsShown().map((x) => x.getAttribute("aria-label"))).toContain("Proposal on #102"));
      expect(next).toHaveBeenLastCalledWith(expect.anything(), { limit: 50, cursor: "cur-1" });
      expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    });

    it("starts over from the first page when the filter changes", async () => {
      const [a, b] = many(2);
      const list = vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async (filter, page) => {
        if (page?.cursor) return { proposals: [b] };
        return filter.priority ? { proposals: [] } : { proposals: [a], nextCursor: "cur-1" };
      });
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      await panel();
      await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
      await vi.waitFor(() => expect(cardsShown()).toHaveLength(2));

      await chooseFromDropdown(userEvent.setup(), "Priority", "high");

      await vi.waitFor(() => expect(cardsShown()).toHaveLength(0));
      expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ priority: ["high"] }), { limit: 50 });
    });

    it("keeps the pages loaded when the list refreshes, even past the server's page limit", async () => {
      const all = many(120);
      let gone = 0;
      // A server that honours limit and cursor over a list a decision can shorten.
      const list = vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async (_filter, page) => {
        const rest = all.slice(gone);
        const from = page?.cursor ? Number(page.cursor) : 0;
        const to = from + (page?.limit ?? 50);
        return { proposals: rest.slice(from, to), nextCursor: to < rest.length ? String(to) : undefined };
      });
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "approveProposal").mockImplementation(async () => {
        gone = 1;
        return { ...all[0], state: "approved" };
      });
      await panel();
      for (const loadedSoFar of [50, 100]) {
        // Text queries, not role queries: computing accessible names across
        // 50-100 cards is what made this test slow enough to time out in CI.
        await userEvent.click(await screen.findByText("Load more"));
        await vi.waitFor(() => expect(list).toHaveBeenCalledWith(expect.anything(), { limit: 50, cursor: String(loadedSoFar) }));
      }
      await vi.waitFor(() => expect(screen.getByText(/act on the 120 shown|act on the 120 loaded/)).toBeInTheDocument());
      list.mockClear();

      await userEvent.click((await screen.findAllByText("Approve"))[0]);

      await vi.waitFor(() => expect(screen.getByText(/act on the 119 shown/)).toBeInTheDocument());
      expect(list.mock.calls.map(([, page]) => page)).toEqual([{ limit: 100 }, { limit: 20, cursor: "100" }]);
    });

    it("says batch buttons act on what is loaded while more is waiting, and names only the loaded ids", async () => {
      const [a, b, c] = many(3);
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      byCursor({ "": { proposals: [a, b], nextCursor: "cur-1" }, "cur-1": { proposals: [c] } });
      const approveAll = vi.spyOn(apiClient, "approveAllProposals").mockResolvedValue([]);
      const rejectAll = vi.spyOn(apiClient, "rejectAllProposals").mockResolvedValue([]);
      await panel();

      expect(await screen.findByText(/act on the 2 loaded, not on the proposals still to load/)).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Approve all loaded to open (2)" }));
      expect(approveAll).toHaveBeenCalledWith("ready", ["p-0", "p-1"]);

      await userEvent.click(screen.getByRole("button", { name: "Reject all loaded (2)" }));
      await userEvent.type(screen.getByLabelText(/Reason/), "no");
      await userEvent.click(screen.getByRole("button", { name: "Reject all loaded 2" }));
      expect(rejectAll).toHaveBeenCalledWith("no", ["p-0", "p-1"]);
    });

    it("sends a batch over the server's id limit in chunks, each id once and in order", async () => {
      const page = many(250);
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: page });
      const approveAll = vi.spyOn(apiClient, "approveAllProposals").mockResolvedValue([]);
      const rejectAll = vi.spyOn(apiClient, "rejectAllProposals").mockResolvedValue([]);
      await panel();
      const ids = page.map((p) => p.id);
      // Text queries on the batch buttons: a role query computes accessible names
      // across the ~130 cards jsdom draws, ~0.3s each unloaded and the cause of
      // this test timing out on a loaded CI runner.

      await userEvent.click(await screen.findByText("Approve all to open (250)"));
      expect(approveAll.mock.calls).toEqual([
        ["ready", ids.slice(0, 100)],
        ["ready", ids.slice(100, 200)],
        ["ready", ids.slice(200)],
      ]);

      await userEvent.click(screen.getByText("Reject all (250)"));
      await userEvent.click(screen.getByText("Reject all 250"));
      expect(rejectAll.mock.calls.map(([, chunk]) => chunk)).toEqual([ids.slice(0, 100), ids.slice(100, 200), ids.slice(200)]);
    });

    it("reloads when a later chunk of a batch fails, so what the earlier ones decided leaves the list", async () => {
      const page = many(101);
      let decidedBefore = 0;
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async () => ({ proposals: page.slice(decidedBefore) }));
      vi.spyOn(apiClient, "approveAllProposals")
        .mockImplementationOnce(async () => {
          decidedBefore = 100;
          return [];
        })
        .mockRejectedValueOnce(new ApiError(500, "database is down"));
      await panel();

      await userEvent.click(await screen.findByRole("button", { name: "Approve all to open (101)" }));

      expect(await screen.findByText("database is down")).toBeInTheDocument();
      expect(await screen.findByRole("button", { name: "Approve all to open (1)" })).toBeInTheDocument();
    });

    it("shows a failed next page on the panel and keeps what is loaded", async () => {
      const [a] = many(1);
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation(async (_filter, page) => {
        if (page?.cursor) throw new ApiError(500, "database is down");
        return { proposals: [a], nextCursor: "cur-1" };
      });
      await panel();

      await userEvent.click(await screen.findByRole("button", { name: "Load more" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
      expect(cardsShown()).toHaveLength(1);
    });
  });

  describe("filter", () => {
    // The provider's own fetch (the badge's, with no filter) interleaves with the panel's.
    const lastFilter = () => vi.mocked(apiClient.listPendingProposalsPage).mock.calls.at(-1)?.[0];

    it("hides proposals on parked, cancelled and done tickets by default", async () => {
      serverList([ready]);
      await panel();
      await screen.findByRole("group", { name: /^Proposal on/ });

      expect(lastFilter()!.status).toEqual(FILTER_STATUSES.filter((st) => !["parked", "cancelled", "done"].includes(st)));
    });

    it("asks the server for the filter's text, priority and dates", async () => {
      serverList([ready]);
      await panel({
        ...DEFAULT_APPROVALS_VALUES,
        text: "login",
        priority: "urgent",
        created: { from: "2026-01-01", to: "" },
        updated: { from: "", to: "2026-02-01" },
      });
      await screen.findByRole("group", { name: /^Proposal on/ });

      expect(lastFilter()).toEqual(
        expect.objectContaining({ q: "login", priority: ["urgent"], created_from: "2026-01-01", updated_to: "2026-02-01" }),
      );
    });

    it("refetches when the filter changes, and shows what comes back", async () => {
      serverList([ready]);
      await panel();
      await screen.findByRole("group", { name: /^Proposal on/ });

      await chooseFromDropdown(userEvent.setup(), "Priority", "high");

      await vi.waitFor(() => expect(lastFilter()).toEqual(expect.objectContaining({ priority: ["high"] })));
    });

    it("shows no cards from the old filter while the new one is loading", async () => {
      let release: (list: Proposal[]) => void = () => {};
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([ready]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockImplementation((filter) =>
        filter.priority
          ? new Promise((resolve) => (release = (list) => resolve({ proposals: list })))
          : Promise.resolve({ proposals: [ready] }),
      );
      await panel();
      await screen.findByRole("group", { name: /^Proposal on/ });

      await chooseFromDropdown(userEvent.setup(), "Priority", "high");

      expect(await screen.findByText("Loading proposals…")).toBeInTheDocument();
      expect(screen.queryByRole("group", { name: /^Proposal on/ })).toBeNull();
      expect(screen.queryByRole("button", { name: /Reject all/ })).toBeNull();
      await act(async () => release([answer]));
      expect(await screen.findByRole("group", { name: /^Proposal on/ })).toHaveAccessibleName("Proposal on #7");
    });

    it("batch approve and reject name the shown proposals' ids, not every pending one", async () => {
      // The server's pending list (what the tab's badge counts) holds a
      // proposal the filter hides; the filtered fetch does not.
      const hidden = makeProposal({ id: "p-hidden", ticket_id: 12, kind: "ready", ticket_status: "parked" });
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([hidden, ready]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: [ready] });
      vi.spyOn(apiClient, "approveAllProposals").mockResolvedValue([]);
      vi.spyOn(apiClient, "rejectAllProposals").mockResolvedValue([]);
      await panel();

      expect(await screen.findByRole("button", { name: "Approve all to open (1)" })).toBeInTheDocument();
      expect(screen.getByText(/act on the 1 shown, not on all 2 pending/)).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Approve all to open (1)" }));
      expect(apiClient.approveAllProposals).toHaveBeenCalledWith("ready", ["p-ready"]);

      await userEvent.click(screen.getByRole("button", { name: "Reject all (1)" }));
      await userEvent.type(screen.getByLabelText(/Reason/), "no");
      await userEvent.click(screen.getByRole("button", { name: "Reject all 1" }));
      expect(apiClient.rejectAllProposals).toHaveBeenCalledWith("no", ["p-ready"]);
    });

    it("says what the filters hide when nothing is shown", async () => {
      vi.spyOn(apiClient, "listProposals").mockResolvedValue([answer]);
      vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: [] });
      await panel();
      expect(await screen.findByText("Nothing shown: 1 pending proposal is hidden by the filters.")).toBeInTheDocument();
    });

    it("asks for nothing when every status is hidden", async () => {
      serverList([ready]);
      await panel({ ...DEFAULT_APPROVALS_VALUES, status: FILTER_STATUSES });
      expect(await screen.findByText("Every status is hidden, so no tickets match.")).toBeInTheDocument();
      expect(apiClient.listPendingProposalsPage).not.toHaveBeenCalled();
    });
  });

  describe("approving an edit while attempts run", () => {
    function approvable(attempts: ReturnType<typeof makeRunningAttempt>[]) {
      serverList([makeEditProposal()]);
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 12, attempts }));
      return vi.spyOn(apiClient, "approveProposal").mockResolvedValue(makeEditProposal());
    }

    it("holds the approval for a warning naming each attempt's revision, and approves on confirm", async () => {
      const approve = approvable([makeRunningAttempt(1, 3), makeRunningAttempt(2, 5)]);
      await panel();
      await userEvent.click(await screen.findByRole("button", { name: "Approve" }));

      expect(
        await screen.findByText(
          "Attempt 1 started from revision 3, attempt 2 from revision 5. Editing changes the spec under them.",
        ),
      ).toBeInTheDocument();
      expect(approve).not.toHaveBeenCalled();

      await userEvent.click(screen.getByRole("button", { name: "Approve anyway" }));
      expect(approve).toHaveBeenCalledWith("p-edit");
    });

    it("approves when the ticket can't be read, without a warning", async () => {
      const approve = approvable([]);
      vi.spyOn(apiClient, "getTicket").mockRejectedValue(new Error("down"));
      await panel();
      await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
      expect(approve).toHaveBeenCalledWith("p-edit");
      expect(screen.queryByText(/Editing changes the spec/)).toBeNull();
    });

    it("approves straight away with one attempt", async () => {
      const approve = approvable([makeRunningAttempt(1, 3)]);
      await panel();
      await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
      expect(approve).toHaveBeenCalledWith("p-edit");
      expect(screen.queryByText(/Editing changes the spec/)).toBeNull();
    });

    it("does not look at the ticket for an edit that leaves the spec alone", async () => {
      serverList([makeEditProposal({ edit: { paths: ["a/"], paths_added: ["a/"], paths_removed: [] } })]);
      const get = vi.spyOn(apiClient, "getTicket");
      const approve = vi.spyOn(apiClient, "approveProposal").mockResolvedValue(makeEditProposal());
      await panel();
      await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
      expect(approve).toHaveBeenCalled();
      expect(get).not.toHaveBeenCalled();
    });
  });
});

describe("ApprovalsPanel steward label", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
  });

  it("shows the label on a card with an assessment and not on one without", async () => {
    serverList([
      makeProposal({
        id: "p-steward",
        ticket_id: 9,
        steward: { category: "merge_continue", tier: "auto", verdict: "would_auto", mode: "shadow", reason: "ok", evidence: [] },
      }),
      ready,
    ]);
    panel();
    expect(await screen.findByText("steward: would auto-approve (merge_continue)")).toBeInTheDocument();
    expect(screen.getAllByText(/^steward:/)).toHaveLength(1);
  });
});
