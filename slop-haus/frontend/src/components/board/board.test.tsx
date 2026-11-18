import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeAttemptCard, makeProfile, makeTicket } from "@/test/fixtures";
import { mockOffsetSize } from "@/test/layout_mock";
import { renderSignedIn } from "@/test/render_signed_in";
import Board, { type ColumnPaging } from "./board";
import type { Status } from "@/api";

const LIVE: Status[] = ["preparing", "open", "in_progress", "blocked", "discussion", "review"];
const WITH_DONE: Status[] = [...LIVE, "done"];
const WITH_PARKED: Status[] = ["parked", ...LIVE];

describe("Board", () => {
  it("contains a card that fails to draw to its own column", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const bad = makeTicket({ id: 1, title: "Bad", status: "open" });
    Object.defineProperty(bad, "title", {
      get() {
        throw new Error("cannot draw");
      },
    });
    const good = makeTicket({ id: 2, title: "Fine ticket", status: "blocked" });

    await renderSignedIn(<Board tickets={[bad, good]} statuses={LIVE} />);

    const open = screen.getByRole("heading", { name: "Open" }).closest(".board__column") as HTMLElement;
    expect(within(open).getByRole("alert")).toHaveTextContent("could not be shown");
    expect(screen.getByText("Fine ticket")).toBeInTheDocument();
  });

  it("groups tickets into the right status columns", async () => {
    const tickets = [
      makeTicket({ id: 1, title: "Open ticket", status: "open" }),
      makeTicket({ id: 2, title: "In progress ticket", status: "in_progress" }),
      makeTicket({ id: 3, title: "Blocked ticket", status: "blocked" }),
      makeTicket({ id: 4, title: "Done ticket", status: "done" }),
      makeTicket({ id: 5, title: "Second open ticket", status: "open" }),
      makeTicket({ id: 6, title: "Discussion ticket", status: "discussion" }),
      makeTicket({ id: 7, title: "Preparing ticket", status: "preparing" }),
      makeTicket({ id: 8, title: "Review ticket", status: "review" }),
    ];

    await renderSignedIn(<Board tickets={tickets} statuses={WITH_DONE} />);

    function column(name: string): HTMLElement {
      const heading = screen.getByRole("heading", { name });
      return heading.closest(".board__column") as HTMLElement;
    }

    expect(within(column("Preparing")).getByText("Preparing ticket")).toBeInTheDocument();
    expect(within(column("Preparing")).queryByText("Open ticket")).not.toBeInTheDocument();
    expect(within(column("Open")).getByText("Open ticket")).toBeInTheDocument();
    expect(within(column("Open")).getByText("Second open ticket")).toBeInTheDocument();
    expect(within(column("Open")).queryByText("Preparing ticket")).not.toBeInTheDocument();
    expect(within(column("In progress")).getByText("In progress ticket")).toBeInTheDocument();
    expect(within(column("In progress")).queryByText("Open ticket")).not.toBeInTheDocument();
    expect(within(column("Blocked")).getByText("Blocked ticket")).toBeInTheDocument();
    expect(within(column("Discussion")).getByText("Discussion ticket")).toBeInTheDocument();
    expect(within(column("Discussion")).queryByText("Blocked ticket")).not.toBeInTheDocument();
    expect(within(column("Review")).getByText("Review ticket")).toBeInTheDocument();
    expect(within(column("Review")).queryByText("Discussion ticket")).not.toBeInTheDocument();
    expect(within(column("Done")).getByText("Done ticket")).toBeInTheDocument();
  });

  // Column order matters as much as membership -- Preparing reads as "not
  // yet in the queue", so it belongs before Open, the first column a ticket
  // becomes claimable in. Discussion and Review both read as "not claimable
  // right now" like Blocked, so they belong next to it rather than at
  // either end.
  it("places Preparing first, and Discussion/Review after Blocked and before Done", async () => {
    await renderSignedIn(<Board tickets={[]} statuses={WITH_DONE} />);

    const headings = screen.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Preparing", "Open", "In progress", "Blocked", "Discussion", "Review", "Done"]);
  });

  it("does not render a column for cancelled tickets", async () => {
    const tickets = [makeTicket({ id: 1, title: "Cancelled ticket", status: "cancelled" })];

    await renderSignedIn(<Board tickets={tickets} statuses={WITH_DONE} />);

    expect(screen.queryByText("Cancelled ticket")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Cancelled" })).not.toBeInTheDocument();
  });

  it("opens the ticket, naming the board tab and the session's tenant, when its card is clicked", async () => {
    const user = userEvent.setup();
    const tickets = [makeTicket({ id: 42, title: "Click me", status: "open" })];

    const { router } = await renderSignedIn(<Board tickets={tickets} statuses={WITH_DONE} />, {
      profile: makeProfile({ tenant_id: "tenant-a" }),
    });
    await user.click(screen.getByText("Click me"));

    expect(router.state.location.href).toBe("/tickets/42?from=board&tenant=tenant-a");
  });

  it("leaves the Done column off unless done tickets are included", async () => {
    await renderSignedIn(<Board tickets={[]} statuses={LIVE} />);

    const headings = screen.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Preparing", "Open", "In progress", "Blocked", "Discussion", "Review"]);
  });

  // Parked is work nobody plans to do: off the board unless asked for, and
  // then first, before Preparing -- which is where unparking puts it.
  it("shows a Parked column, first, only when parked tickets are included", async () => {
    const tickets = [makeTicket({ id: 1, title: "Someday", status: "parked" })];

    const { unmount } = await renderSignedIn(<Board tickets={tickets} statuses={LIVE} />);
    expect(screen.queryByRole("heading", { name: "Parked" })).not.toBeInTheDocument();
    expect(screen.queryByText("Someday")).not.toBeInTheDocument();
    unmount();

    await renderSignedIn(<Board tickets={tickets} statuses={WITH_PARKED} />);
    const headings = screen.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Parked", "Preparing", "Open", "In progress", "Blocked", "Discussion", "Review"]);
    const parked = screen.getByRole("heading", { name: "Parked" }).closest(".board__column") as HTMLElement;
    expect(within(parked).getByText("Someday")).toBeInTheDocument();
  });

  // The phone board's column picker. Which column the stylesheet shows is
  // the --shown class; jsdom applies no stylesheet, so e2e's layout.spec.ts
  // is what checks that only that one is on screen.
  describe("column chips", () => {
    function chip(name: string): HTMLElement {
      return screen.getByRole("button", { name: new RegExp(`^${name} \\d+$`) });
    }

    it("offers one chip per column, each with its count", async () => {
      const tickets = [
        makeTicket({ id: 1, status: "open" }),
        makeTicket({ id: 2, status: "open" }),
        makeTicket({ id: 3, status: "blocked" }),
      ];
      await renderSignedIn(<Board tickets={tickets} statuses={LIVE} />);

      const group = screen.getByRole("group", { name: "Board column" });
      expect(within(group).getAllByRole("button").map((b) => b.textContent)).toEqual([
        "Preparing 0",
        "Open 2",
        "In progress 0",
        "Blocked 1",
        "Discussion 0",
        "Review 0",
      ]);
    });

    it("marks the column asked for as the one shown", async () => {
      const tickets = [makeTicket({ id: 1, title: "Blocked one", status: "blocked" })];
      await renderSignedIn(<Board tickets={tickets} statuses={LIVE} column="blocked" />);

      expect(chip("Blocked")).toHaveAttribute("aria-pressed", "true");
      expect(chip("Open")).toHaveAttribute("aria-pressed", "false");
      const shown = document.querySelectorAll(".board__column--shown");
      expect(shown).toHaveLength(1);
      expect(within(shown[0] as HTMLElement).getByText("Blocked one")).toBeInTheDocument();
    });

    it("shows the default column when none is asked for", async () => {
      await renderSignedIn(<Board tickets={[makeTicket({ status: "review" })]} statuses={LIVE} />);

      expect(chip("Review")).toHaveAttribute("aria-pressed", "true");
    });

    it("asks for the column whose chip is pressed", async () => {
      const onColumnChange = vi.fn();
      const user = userEvent.setup();
      await renderSignedIn(<Board tickets={[]} statuses={LIVE} onColumnChange={onColumnChange} />);

      await user.click(chip("Discussion"));

      expect(onColumnChange).toHaveBeenCalledWith("discussion");
    });
  });

  describe("paged columns", () => {
    const paging = (over: Partial<ColumnPaging> = {}): ColumnPaging => ({
      loading: false,
      hasMore: false,
      loadingMore: false,
      error: null,
      onLoadMore: vi.fn(),
      onRetry: vi.fn(),
      ...over,
    });

    it("offers Load more on a column with more to come, and counts it as a lower bound", async () => {
      const onLoadMore = vi.fn();
      const user = userEvent.setup();
      const tickets = [makeTicket({ id: 1, title: "Open one", status: "open" })];
      await renderSignedIn(<Board tickets={tickets} statuses={LIVE} paging={{ open: paging({ hasMore: true, onLoadMore }) }} />);

      const open = screen.getByRole("heading", { name: "Open" }).closest(".board__column") as HTMLElement;
      expect(within(open).getByText("1+")).toBeInTheDocument();
      await user.click(within(open).getByRole("button", { name: "Load more" }));

      expect(onLoadMore).toHaveBeenCalledTimes(1);
      // The other columns are complete as given.
      expect(screen.getAllByRole("button", { name: "Load more" })).toHaveLength(1);
    });

    it("says Select all covers only what is loaded while a column has more to come", async () => {
      const selection = { selected: new Set<number>(), onToggle: vi.fn(), onSelectAll: vi.fn(), onClear: vi.fn() };
      const tickets = [makeTicket({ id: 1, status: "open" }), makeTicket({ id: 2, status: "blocked" })];
      await renderSignedIn(<Board tickets={tickets} statuses={LIVE} selection={selection} paging={{ open: paging({ hasMore: true }) }} />);

      expect(screen.getByRole("button", { name: "Select all loaded in Open" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Select all in Blocked" })).toBeInTheDocument();
    });

    it("says a column is loading rather than empty until its first page arrives", async () => {
      await renderSignedIn(<Board tickets={[]} statuses={LIVE} paging={{ review: paging({ loading: true }) }} />);

      const review = screen.getByRole("heading", { name: "Review" }).closest(".board__column") as HTMLElement;
      expect(within(review).getByText("Loading…")).toBeInTheDocument();
      expect(within(review).queryByText("No tickets")).not.toBeInTheDocument();
    });

    it("shows a column's own error, with Try again for that column only", async () => {
      const onRetry = vi.fn();
      const user = userEvent.setup();
      await renderSignedIn(<Board tickets={[]} statuses={LIVE} paging={{ blocked: paging({ error: "database is down", onRetry }) }} />);

      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("database is down");
      await user.click(within(alert).getByRole("button", { name: "Try again" }));

      expect(onRetry).toHaveBeenCalledTimes(1);
    });

    // The Done column is drawn through VirtualList: of a thousand loaded
    // cards only a window is in the page.
    it("draws only the cards on screen in the Done column", async () => {
      // jsdom reports every height as 0, which would put all the rows in
      // the window; give the viewport and the cards the heights a browser would.
      mockOffsetSize({
        height: (el) => (el.classList.contains("virtual-list__viewport") ? 600 : el.hasAttribute("data-index") ? 120 : 0),
      });
      const done = Array.from({ length: 1000 }, (_, i) => makeTicket({ id: 5000 - i, title: `Finished ${5000 - i}`, status: "done" }));
      await renderSignedIn(<Board tickets={done} statuses={WITH_DONE} paging={{ done: paging({ hasMore: true }) }} />);

      const column = screen.getByRole("heading", { name: "Done" }).closest(".board__column") as HTMLElement;
      expect(within(column).getByText("Finished 5000")).toBeInTheDocument();
      expect(within(column).queryByText("Finished 4001")).not.toBeInTheDocument();
      expect(within(column).getByRole("region", { name: "Done tickets" })).toBeInTheDocument();
      expect(within(column).getByRole("button", { name: "Load more" })).toBeInTheDocument();
    });
  });

  describe("attempt cards", () => {
    function column(name: string): HTMLElement {
      return screen.getByRole("heading", { name }).closest(".board__column") as HTMLElement;
    }

    // Attempt 1 finished and awaits a choice in Review while attempt 2 is
    // still in progress: two cards of one ticket, in two columns.
    const attempts = [
      makeAttemptCard({ id: 42, title: "Two ways", status: "in_progress" }, { number: 2, total: 2 }),
      makeAttemptCard({ id: 42, title: "Two ways", status: "review" }, { number: 1, total: 2, awaiting_choice: true }),
    ];

    it("draws one card per attempt, each in its own column", async () => {
      await renderSignedIn(<Board tickets={attempts} statuses={LIVE} />);

      expect(within(column("In progress")).getAllByText("Two ways")).toHaveLength(1);
      expect(within(column("In progress")).getByText(/#42/)).toHaveTextContent("#42 · 2");
      expect(within(column("Review")).getAllByText("Two ways")).toHaveLength(1);
      expect(within(column("Review")).getByText(/#42/)).toHaveTextContent("#42 · 1");
      expect(within(column("Review")).getByText("awaiting choice")).toBeInTheDocument();
      expect(within(column("In progress")).queryByText("awaiting choice")).not.toBeInTheDocument();
    });

    it("draws both attempts of a ticket in one column as two cards, never stacked into one", async () => {
      const both = [
        makeAttemptCard({ id: 42, title: "Two ways", status: "review" }, { number: 2, awaiting_choice: true }),
        makeAttemptCard({ id: 42, title: "Two ways", status: "review" }, { number: 1, awaiting_choice: true }),
      ];
      await renderSignedIn(<Board tickets={both} statuses={LIVE} />);

      expect(within(column("Review")).getAllByText("Two ways")).toHaveLength(2);
      expect(column("Review")).toHaveTextContent("Review2");
    });

    it("selects the ticket from either card, marking both and counting it once in select-all", async () => {
      const user = userEvent.setup();
      const both = [
        makeAttemptCard({ id: 42, status: "review" }, { number: 2 }),
        makeAttemptCard({ id: 42, status: "review" }, { number: 1 }),
      ];
      const onToggle = vi.fn();
      const onSelectAll = vi.fn();
      await renderSignedIn(
        <Board
          tickets={both}
          statuses={LIVE}
          selection={{ selected: new Set([42]), onToggle, onSelectAll, onClear: vi.fn() }}
        />,
      );

      const cards = within(column("Review")).getAllByRole("button", { pressed: true });
      expect(cards).toHaveLength(2);
      await user.click(cards[0]);
      expect(onToggle).toHaveBeenCalledWith(42);
      await user.click(within(column("Review")).getByRole("button", { name: "Clear Review" }));
      await user.click(within(column("Review")).getByRole("button", { name: "Select all in Review" }));
      expect(onSelectAll).not.toHaveBeenCalled();
    });
  });
});
