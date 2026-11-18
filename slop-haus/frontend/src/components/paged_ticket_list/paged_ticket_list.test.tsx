import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeTicket } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import type { PagedCollectionView } from "@/lib/paged_collection/use_paged_collection";
import PagedTicketList, { PagedTicketRow } from "./paged_ticket_list";

function makeList(overrides: Partial<PagedCollectionView> = {}): PagedCollectionView {
  return {
    items: [],
    loading: false,
    loadingMore: false,
    hasMore: false,
    loaded: true,
    error: null,
    loadMore: vi.fn(),
    retry: vi.fn(),
    reload: vi.fn(),
    ...overrides,
  };
}

function view(list: PagedCollectionView, extra: { filtered?: boolean; matchesNothingMessage?: string } = {}) {
  return (
    <PagedTicketList
      className="page"
      list={list}
      filter={<div>the filter</div>}
      intro={<p>the intro</p>}
      filtered={extra.filtered ?? false}
      matchesNothingMessage={extra.matchesNothingMessage}
      loadingMessage="Loading things…"
      emptyMessage={{ filtered: "Nothing matches.", unfiltered: "Nothing at all." }}
      label="Things"
      estimateRowHeight={50}
      renderRow={(ticket) => (
        <PagedTicketRow
          ticketId={ticket.id}
          from="blocked"
          link={ticket.title}
          detail={ticket.id === 1 && <div>detail for 1</div>}
        >
          <span>cell {ticket.id}</span>
        </PagedTicketRow>
      )}
    />
  );
}

const tickets = [makeTicket({ id: 1, title: "First" }), makeTicket({ id: 2, title: "Second" })];

describe("PagedTicketList", () => {
  it("draws the intro and filter in every state", async () => {
    await renderSignedIn(view(makeList({ loaded: false })));
    expect(screen.getByText("the intro")).toBeInTheDocument();
    expect(screen.getByText("the filter")).toBeInTheDocument();
  });

  it("shows the loading message until the first page arrives", async () => {
    await renderSignedIn(view(makeList({ loaded: false })));
    expect(screen.getByText("Loading things…")).toBeInTheDocument();
  });

  it("words the empty message by whether a filter is on", async () => {
    const { unmount } = await renderSignedIn(view(makeList()));
    expect(screen.getByText("Nothing at all.")).toBeInTheDocument();
    unmount();
    await renderSignedIn(view(makeList(), { filtered: true }));
    expect(screen.getByText("Nothing matches.")).toBeInTheDocument();
  });

  it("shows the matches-nothing message ahead of every other state", async () => {
    await renderSignedIn(view(makeList({ loaded: false }), { matchesNothingMessage: "All hidden." }));
    expect(screen.getByText("All hidden.")).toBeInTheDocument();
    expect(screen.queryByText("Loading things…")).not.toBeInTheDocument();
  });

  it("draws each row's link, cells and detail inside the shared shell", async () => {
    const { container } = await renderSignedIn(view(makeList({ items: tickets })));
    // The pages take the window's height; the layout itself is checked in the e2e suite.
    expect(container.querySelector(".virtual-list")).toHaveClass("virtual-list--fill");
    expect(screen.getByRole("link", { name: "First" })).toHaveClass("paged-ticket-list__ticket");
    expect(screen.getByText("cell 2")).toBeInTheDocument();
    expect(screen.getByText("detail for 1")).toBeInTheDocument();
    expect(screen.queryByText("Loading things…")).not.toBeInTheDocument();
  });

  it("offers Load more while there are more pages, and asks for the next", async () => {
    const list = makeList({ items: tickets, hasMore: true });
    await renderSignedIn(view(list));
    await userEvent.setup().click(screen.getByRole("button", { name: "Load more" }));
    expect(list.loadMore).toHaveBeenCalledTimes(1);
  });

  it("shows an error alone when the first page failed, and Try again retries", async () => {
    const list = makeList({ loaded: false, error: "boom" });
    await renderSignedIn(view(list));
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
    expect(screen.queryByText("Loading things…")).not.toBeInTheDocument();
    expect(screen.queryByText("Nothing at all.")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(list.retry).toHaveBeenCalledTimes(1);
  });

  it("keeps the rows it has when a later page fails", async () => {
    await renderSignedIn(view(makeList({ items: tickets, error: "later page failed" })));
    expect(screen.getByText("First")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("later page failed");
  });
});
