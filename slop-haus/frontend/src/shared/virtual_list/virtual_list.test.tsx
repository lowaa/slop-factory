import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockOffsetSize } from "@/test/layout_mock";
import VirtualList from "./virtual_list";
import type { VirtualListProps } from "./virtual_list";

const ROW = 50;
const VIEWPORT = 200;

function rows(count: number): string[] {
  return Array.from({ length: count }, (_, i) => `row ${i}`);
}

function listProps(overrides: Partial<VirtualListProps<string>>): VirtualListProps<string> {
  return {
    items: rows(1000),
    getKey: (item) => item,
    renderRow: (item) => <button type="button">{item}</button>,
    label: "Tickets",
    estimateRowHeight: ROW,
    viewportHeight: VIEWPORT,
    overscan: 1,
    ...overrides,
  };
}

function renderList(overrides: Partial<VirtualListProps<string>> = {}) {
  const result = render(<VirtualList {...listProps(overrides)} />);
  // Re-renders with the same props plus these, as a parent's state change would.
  const update = (next: Partial<VirtualListProps<string>>) =>
    result.rerender(<VirtualList {...listProps({ ...overrides, ...next })} />);
  return { ...result, update };
}

function drawn(): string[] {
  return screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
}

function scrollTo(top: number) {
  const viewport = screen.getByRole("region", { name: "Tickets" });
  viewport.scrollTop = top;
  fireEvent.scroll(viewport);
}

describe("VirtualList windowing", () => {
  it("draws only the rows in the viewport plus overscan", () => {
    renderList();

    // 200px / 50px = rows 0-3 visible, one more of overscan.
    expect(drawn()).toEqual(["row 0", "row 1", "row 2", "row 3", "row 4"]);
  });

  it("places each row at its offset in the full list", () => {
    renderList();
    expect(screen.getByText("row 3").closest("li")).toHaveStyle({ transform: `translateY(${3 * ROW}px)` });

    scrollTo(500 * ROW);

    expect(screen.getByText("row 500").closest("li")).toHaveStyle({ transform: `translateY(${500 * ROW}px)` });
  });

  it("sizes the scroll area for every row, so the scrollbar is honest", () => {
    renderList();

    expect(screen.getByRole("list")).toHaveStyle({ height: `${1000 * ROW}px` });
  });

  it("draws the rows at the scroll position after scrolling", () => {
    renderList();

    scrollTo(500 * ROW);

    expect(drawn()).toEqual(["row 499", "row 500", "row 501", "row 502", "row 503", "row 504"]);
    expect(screen.queryByText("row 0")).not.toBeInTheDocument();
  });
});

describe("VirtualList semantics", () => {
  it("names the list and gives each row its place in the full set", () => {
    renderList();

    const region = screen.getByRole("region", { name: "Tickets" });
    const items = within(region).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("aria-posinset"))).toEqual(["1", "2", "3", "4", "5"]);
    for (const li of items) expect(li).toHaveAttribute("aria-setsize", "1000");
  });

  it("keeps positions absolute once scrolled, not relative to the window", () => {
    renderList();

    scrollTo(500 * ROW);

    expect(screen.getByText("row 500").closest("li")).toHaveAttribute("aria-posinset", "501");
  });

  it("uses totalCount for the set size when more exist than are loaded", () => {
    renderList({ items: rows(100), totalCount: 5000, hasMore: true });

    expect(screen.getAllByRole("listitem")[0]).toHaveAttribute("aria-setsize", "5000");
  });

  it("says the size is unknown while more can load and no total is given", () => {
    renderList({ items: rows(100), hasMore: true });

    expect(screen.getAllByRole("listitem")[0]).toHaveAttribute("aria-setsize", "-1");
  });

  it("reports busy only while loading", () => {
    const { update } = renderList({ hasMore: true, loading: true });
    expect(screen.getByRole("list")).toHaveAttribute("aria-busy", "true");

    update({ loading: false });
    expect(screen.getByRole("list")).toHaveAttribute("aria-busy", "false");
  });

  it("makes the viewport focusable so the keyboard can scroll it", () => {
    renderList();

    expect(screen.getByRole("region", { name: "Tickets" })).toHaveAttribute("tabindex", "0");
  });
});

describe("VirtualList fill", () => {
  it("is not filling by default", () => {
    const { container } = renderList({ viewportHeight: undefined });

    expect(container.firstElementChild).not.toHaveClass("virtual-list--fill");
    expect(screen.getByRole("region", { name: "Tickets" }).style.height).toBe("");
  });

  it("marks the list as filling, leaving the height to the layout", () => {
    const { container } = renderList({ viewportHeight: undefined, fill: true });

    expect(container.firstElementChild).toHaveClass("virtual-list--fill");
    expect(screen.getByRole("region", { name: "Tickets" }).style.height).toBe("");
  });
});

describe("VirtualList focus", () => {
  it("keeps a focused row rendered after it scrolls out of the window", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "row 2" }));
    expect(screen.getByRole("button", { name: "row 2" })).toHaveFocus();

    scrollTo(500 * ROW);

    const row = screen.getByRole("button", { name: "row 2" });
    expect(row).toHaveFocus();
    expect(row.closest("li")).toHaveAttribute("aria-posinset", "3");
  });

  it("follows the focused row when rows are inserted above it", async () => {
    const user = userEvent.setup();
    const { update } = renderList();
    await user.click(screen.getByRole("button", { name: "row 2" }));
    scrollTo(500 * ROW);

    update({ items: ["new a", "new b", ...rows(1000)] });

    const row = screen.getByRole("button", { name: "row 2" });
    expect(row).toHaveFocus();
    expect(row.closest("li")).toHaveAttribute("aria-posinset", "5");
  });

  it("drops the row once focus leaves the list", async () => {
    const user = userEvent.setup();
    renderList({
      renderRow: (item) => <button type="button">{item}</button>,
    });
    await user.click(screen.getByRole("button", { name: "row 2" }));
    scrollTo(500 * ROW);

    await user.click(document.body);

    expect(screen.queryByRole("button", { name: "row 2" })).not.toBeInTheDocument();
  });

  it("finds the focused row where it was when scrolled back", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "row 2" }));
    scrollTo(500 * ROW);

    scrollTo(0);

    expect(screen.getByRole("button", { name: "row 2" })).toHaveFocus();
    expect(drawn()).toEqual(["row 0", "row 1", "row 2", "row 3", "row 4"]);
  });
});

describe("VirtualList infinite scroll", () => {
  it("asks for more when the window nears the end of the loaded rows", () => {
    const onLoadMore = vi.fn();
    renderList({ items: rows(20), hasMore: true, onLoadMore, loadAhead: 2 });
    expect(onLoadMore).not.toHaveBeenCalled();

    scrollTo(20 * ROW);

    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("does not ask again for the same rows after a load that added nothing", () => {
    const onLoadMore = vi.fn();
    const { update } = renderList({ items: rows(5), hasMore: true, onLoadMore });
    expect(onLoadMore).toHaveBeenCalledTimes(1);

    update({ loading: true });
    update({ loading: false });

    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("does not ask while a load is in flight or nothing is left", () => {
    const onLoadMore = vi.fn();
    const { update } = renderList({ items: rows(5), hasMore: true, loading: true, onLoadMore });
    expect(onLoadMore).not.toHaveBeenCalled();

    update({ hasMore: false });
    expect(onLoadMore).not.toHaveBeenCalled();
  });

  it("shows no Load more button", () => {
    renderList({ items: rows(20), hasMore: true });

    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("VirtualList load-ahead", () => {
  // 20 rows, 4 in view: scrolling to row N puts row N+3 last in view, and the trigger is row 16 (19 - 3).
  it("asks once the window is within loadAhead rows of the end, not before", () => {
    const onLoadMore = vi.fn();
    renderList({ items: rows(20), hasMore: true, onLoadMore, loadAhead: 3 });

    scrollTo(12 * ROW);
    expect(onLoadMore).not.toHaveBeenCalled();

    scrollTo(13 * ROW);
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("asks for the first page when nothing is loaded yet", () => {
    const onLoadMore = vi.fn();
    renderList({ items: [], hasMore: true, onLoadMore });

    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("asks again after the user scrolls away from the end and back, as a retry", () => {
    const onLoadMore = vi.fn();
    renderList({ items: rows(20), hasMore: true, onLoadMore, loadAhead: 3 });
    scrollTo(20 * ROW);
    expect(onLoadMore).toHaveBeenCalledTimes(1);

    scrollTo(0);
    scrollTo(20 * ROW);

    expect(onLoadMore).toHaveBeenCalledTimes(2);
  });

  it("asks again for a refreshed list of the same length", () => {
    const onLoadMore = vi.fn();
    const { update } = renderList({ items: rows(5), hasMore: true, onLoadMore });
    expect(onLoadMore).toHaveBeenCalledTimes(1);

    update({ items: rows(5).map((row) => `new ${row}`) });

    expect(onLoadMore).toHaveBeenCalledTimes(2);
  });
});

describe("VirtualList measured rows", () => {
  it("lays rows out by their measured heights, not the estimate", () => {
    // The virtualizer reads a row's offsetHeight, which jsdom reports as 0.
    mockOffsetSize({ height: (el) => (Number(el.getAttribute("data-index")) % 2 === 0 ? 100 : 20) });

    renderList({ items: rows(10), measureRows: true, estimateRowHeight: ROW, overscan: 10 });

    // 5 rows of 100 and 5 of 20, where the estimate alone would say 500.
    expect(screen.getByRole("list")).toHaveStyle({ height: "600px" });
    expect(screen.getByText("row 2").closest("li")).toHaveStyle({ transform: "translateY(120px)" });
  });
});

describe("VirtualList load more button", () => {
  it("never loads on scroll, only on the button", async () => {
    const user = userEvent.setup();
    const onLoadMore = vi.fn();
    renderList({ items: rows(20), hasMore: true, mode: "button", onLoadMore });

    scrollTo(20 * ROW);
    expect(onLoadMore).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("is disabled and says so while loading", () => {
    renderList({ items: rows(20), hasMore: true, loading: true, mode: "button" });

    expect(screen.getByRole("button", { name: "Loading..." })).toBeDisabled();
  });

  it("goes away when nothing is left", () => {
    renderList({ items: rows(20), hasMore: false, mode: "button" });

    expect(screen.queryByRole("button", { name: /Load more|Loading/ })).not.toBeInTheDocument();
  });
});

describe("VirtualList announcements", () => {
  it("is silent on first render", () => {
    renderList();

    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("announces how many rows arrived", () => {
    const { update } = renderList({ items: rows(100), hasMore: true, totalCount: 500 });

    update({ items: rows(150) });

    expect(screen.getByRole("status")).toHaveTextContent("50 more rows loaded, 150 of 500 shown");
  });

  it("goes quiet again when the list is replaced by a shorter one", () => {
    const { update } = renderList({ items: rows(100) });
    update({ items: rows(150) });
    update({ items: rows(10) });

    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });
});
