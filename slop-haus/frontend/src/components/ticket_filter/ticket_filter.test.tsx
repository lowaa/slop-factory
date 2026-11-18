import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { SortModel, type SortId } from "@/lib/ticket_query/ticket_sort";
import { apiClient } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { stubNarrowLayout } from "@/test/layout";
import { renderSignedIn } from "@/test/render_signed_in";
import { APPROVALS_FILTER_MODEL } from "@/domain/approvals_filter";
import { BOARD_FILTER_MODEL } from "@/domain/board_filter";
import TicketFilter from "./ticket_filter";
import { TEXT_DEBOUNCE_MS } from "./use_ticket_filter";
import { TicketFilterModel, type FilterValues } from "@/domain/ticket_filter_model";

function values(done: boolean, parked: boolean, text: string): FilterValues {
  return { text, done, parked };
}

// A caller that applies a change only when told to, the way BoardRoute's
// navigate lands a moment after it is asked for: what the box shows has to
// survive its own earlier text arriving late.
let apply: (filter: FilterValues) => void;
const onChange = vi.fn<(filter: FilterValues) => void>();

function DeferredCaller() {
  const [filter, setFilter] = useState<FilterValues>(values(false, false, ""));
  apply = setFilter;
  return <TicketFilter model={BOARD_FILTER_MODEL} values={filter} onChange={onChange} />;
}

function searchbox(): HTMLElement {
  return screen.getByRole("searchbox", { name: "Search tickets" });
}

// fireEvent rather than user-event throughout: user-event's async wrapper
// waits on a real setTimeout(0), which never fires under fake timers, and
// fake timers are what make "once typing pauses" testable without waiting
// out the debounce for real.
function type(text: string): void {
  fireEvent.change(searchbox(), { target: { value: text } });
}

// Synchronous, for the same reason as type(): user-event would wait on the
// faked timers.
function choose(label: string, option: string): void {
  fireEvent.click(screen.getByRole("combobox", { name: label }));
  fireEvent.mouseDown(screen.getByRole("option", { name: option }));
}

function pause(): void {
  act(() => vi.advanceTimersByTime(TEXT_DEBOUNCE_MS));
}

describe("TicketFilter", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    onChange.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("hands on the text once typing pauses", () => {
    render(<DeferredCaller />);

    type("z");
    act(() => vi.advanceTimersByTime(TEXT_DEBOUNCE_MS - 1));
    type("ze");
    act(() => vi.advanceTimersByTime(TEXT_DEBOUNCE_MS - 1));
    type("zeb");
    expect(onChange).not.toHaveBeenCalled();

    pause();
    expect(onChange).toHaveBeenCalledExactlyOnceWith(values(false, false, "zeb"));
  });

  it("keeps what was typed after a pause when that pause's text arrives back", () => {
    render(<DeferredCaller />);
    type("zeb");
    pause();

    type("zebra");
    act(() => apply(values(false, false, "zeb")));

    expect(searchbox()).toHaveValue("zebra");
    pause();
    expect(onChange).toHaveBeenLastCalledWith(values(false, false, "zebra"));
  });

  it("shows a text set from elsewhere in place of the one typed", () => {
    render(<DeferredCaller />);
    type("zebra");
    pause();
    act(() => apply(values(false, false, "zebra")));

    // The header's Board tab: the address, and so the filter, is cleared.
    act(() => apply(values(false, false, "")));

    expect(searchbox()).toHaveValue("");
    pause();
    expect(onChange).toHaveBeenCalledTimes(1);

    // Typing the cleared text again is a change like any other.
    type("zebra");
    pause();
    expect(onChange).toHaveBeenCalledTimes(2);
    expect(onChange).toHaveBeenLastCalledWith(values(false, false, "zebra"));
  });

  it("changes Include done straight away, keeping the text", () => {
    render(<DeferredCaller />);
    act(() => apply(values(false, false, "zebra")));

    fireEvent.click(screen.getByRole("checkbox", { name: "Include done" }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith(values(true, false, "zebra"));
  });

  it("takes the typed text along with Include done, whether or not it has been handed on", () => {
    render(<DeferredCaller />);
    type("zebra");
    pause();
    // Handed on, but not back yet: the change below must not undo it.
    fireEvent.click(screen.getByRole("checkbox", { name: "Include done" }));
    expect(onChange).toHaveBeenLastCalledWith(values(true, false, "zebra"));

    act(() => apply(values(true, false, "zebra")));
    type("zebra import");
    // Not handed on yet: ticking the box hands it on now, and once.
    fireEvent.click(screen.getByRole("checkbox", { name: "Include done" }));
    expect(onChange).toHaveBeenLastCalledWith(values(false, false, "zebra import"));
    act(() => apply(values(false, false, "zebra import")));
    pause();
    expect(onChange).toHaveBeenCalledTimes(3);
    expect(searchbox()).toHaveValue("zebra import");
  });

  it("changes Include parked straight away, keeping the text and Include done", () => {
    render(<DeferredCaller />);
    act(() => apply(values(true, false, "zebra")));

    fireEvent.click(screen.getByRole("checkbox", { name: "Include parked" }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith(values(true, true, "zebra"));
    act(() => apply(values(true, true, "zebra")));
    expect(screen.getByRole("checkbox", { name: "Include parked" })).toBeChecked();

    // And Include done leaves it as it is.
    fireEvent.click(screen.getByRole("checkbox", { name: "Include done" }));
    expect(onChange).toHaveBeenLastCalledWith(values(false, true, "zebra"));
  });
});

// The jump to a ticket typed as "#N": real timers, since the router the
// link needs settles on its own.
describe("TicketFilter' jump to a ticket", () => {
  const noFilter = values(false, false, "");

  function Caller() {
    const [filter, setFilter] = useState<FilterValues>(noFilter);
    return (
      <TicketFilter
        model={BOARD_FILTER_MODEL}
        values={filter}
        onChange={(f) => {
          onChange(f);
          setFilter(f);
        }}
      />
    );
  }

  beforeEach(() => {
    onChange.mockReset();
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 85, title: "Jump to a ticket", status: "done" }));
  });

  function offer() {
    return screen.findByRole("link", { name: /#85.*Jump to a ticket/ });
  }

  it("opens the offered ticket on Enter", async () => {
    const { router } = await renderSignedIn(<Caller />);
    type("#85");
    await offer();

    const enter = fireEvent.keyDown(searchbox(), { key: "Enter" });

    expect(enter).toBe(false);
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/tickets/85"));
    expect(router.state.location.search).toMatchObject({ from: "board" });
  });

  it("leaves the Enter that ends an IME composition to the composition", async () => {
    const { router } = await renderSignedIn(<Caller />);
    type("#85");
    await offer();

    const enter = fireEvent.keyDown(searchbox(), { key: "Enter", isComposing: true });

    expect(enter).toBe(true);
    expect(router.state.location.pathname).toBe("/");
  });

  it("stays put on Enter while nothing is offered", async () => {
    const { router } = await renderSignedIn(<Caller />);
    type("zebra");

    const enter = fireEvent.keyDown(searchbox(), { key: "Enter" });

    expect(enter).toBe(true);
    expect(router.state.location.pathname).toBe("/");
    expect(apiClient.getTicket).not.toHaveBeenCalled();
  });

  it("hides the offer on Escape, keeping the text, until the box changes", async () => {
    const { router } = await renderSignedIn(<Caller />);
    type("#85");
    await offer();

    const escape = fireEvent.keyDown(searchbox(), { key: "Escape" });

    expect(escape).toBe(false);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(searchbox()).toHaveValue("#85");
    // Hidden means Enter has nothing to open either.
    fireEvent.keyDown(searchbox(), { key: "Enter" });
    expect(router.state.location.pathname).toBe("/");

    type("#8");
    type("#85");
    expect(await offer()).toBeInTheDocument();
  });

  it("leaves Escape to the search box when nothing is offered", async () => {
    await renderSignedIn(<Caller />);
    type("zebra");

    expect(fireEvent.keyDown(searchbox(), { key: "Escape" })).toBe(true);
  });

  it("still filters the board by the text: the jump is offered on top", async () => {
    await renderSignedIn(<Caller />);
    type("#85");
    await offer();

    await vi.waitFor(() => expect(onChange).toHaveBeenCalledWith(values(false, false, "#85")));
  });
});

describe("TicketFilter's other fields", () => {
  const MODEL = new TicketFilterModel("fields", [
    { type: "text", id: "text", param: "q", label: "Search" },
    { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled"] },
    { type: "priority", id: "priority", param: "priority" },
    { type: "date_range", id: "closed", fromParam: "from", toParam: "to", label: "Merged", dateField: "closed" },
  ]);
  const change = vi.fn<(v: FilterValues) => void>();

  beforeEach(() => change.mockReset());

  function show(current: FilterValues = MODEL.defaults()) {
    return render(<TicketFilter model={MODEL} values={current} onChange={change} />);
  }

  it("applies a status ticked or unticked straight away", () => {
    show();
    expect(screen.getByRole("checkbox", { name: "Parked" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Open" })).toBeChecked();

    fireEvent.click(screen.getByRole("checkbox", { name: "Parked" }));
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ status: ["cancelled"] }));

    fireEvent.click(screen.getByRole("checkbox", { name: "Open" }));
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ status: ["parked", "cancelled", "open"] }));
  });

  it("applies a priority and a date straight away, saying which date it is", () => {
    show();

    choose("Priority", "high");
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ priority: "high" }));

    // "Any" is an option, not an empty selection: it unsets the field as "".
    choose("Priority", "Any");
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ priority: "" }));

    fireEvent.change(screen.getByLabelText("Merged from"), { target: { value: "2026-01-01" } });
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ closed: { from: "2026-01-01", to: "" } }));
  });

  it("keeps the dates in order by bounding each by the other", () => {
    show({ ...MODEL.defaults(), closed: { from: "2026-01-01", to: "2026-02-01" } });

    expect(screen.getByLabelText("Merged from")).toHaveAttribute("max", "2026-02-01");
    expect(screen.getByLabelText("Merged to")).toHaveAttribute("min", "2026-01-01");
  });

  it("leaves a date unbounded while the other is empty", () => {
    show();

    expect(screen.getByLabelText("Merged from")).not.toHaveAttribute("max");
    expect(screen.getByLabelText("Merged to")).not.toHaveAttribute("min");
  });

  it("offers Clear filters only while filtered, and clears the box too", () => {
    const { rerender } = show();
    expect(screen.queryByRole("button", { name: "Clear filters" })).not.toBeInTheDocument();

    rerender(<TicketFilter model={MODEL} values={{ ...MODEL.defaults(), priority: "high" }} onChange={change} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), { target: { value: "half typed" } });
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(change).toHaveBeenCalledExactlyOnceWith(MODEL.defaults());
    expect(screen.getByRole("searchbox", { name: "Search" })).toHaveValue("");
  });

  describe("sort control", () => {
    beforeEach(() => onChange.mockReset());

    const sortModel = new SortModel(["newest", "oldest", "priority"], "newest");

    function renderSortable(value: SortId, filter: FilterValues = values(false, false, "")) {
      const onSortChange = vi.fn<(sort: SortId) => void>();
      const view = render(
        <TicketFilter
          model={BOARD_FILTER_MODEL}
          values={filter}
          onChange={onChange}
          sort={{ model: sortModel, value, onChange: onSortChange }}
        />,
      );
      return { onSortChange, ...view };
    }

    it("offers the page's sorts by name and reports the choice", () => {
      const { onSortChange } = renderSortable("newest");

      fireEvent.click(screen.getByRole("combobox", { name: "Sort by" }));
      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
        "Newest first",
        "Oldest first",
        "Priority (urgent first)",
      ]);
      fireEvent.mouseDown(screen.getByRole("option", { name: "Priority (urgent first)" }));

      expect(onSortChange).toHaveBeenCalledWith("priority");
      expect(onChange).not.toHaveBeenCalled();
    });

    it("does not count a chosen sort as filtering", () => {
      renderSortable("priority");
      expect(screen.getByRole("combobox", { name: "Sort by" })).toHaveTextContent("Priority (urgent first)");
      expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
    });

    it("clears the filter and leaves the sort alone", () => {
      const { onSortChange } = renderSortable("priority", values(true, false, ""));

      fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));

      expect(onChange).toHaveBeenCalledWith(BOARD_FILTER_MODEL.defaults());
      expect(onSortChange).not.toHaveBeenCalled();
    });

    it("shows no control where a page offers a single sort", () => {
      render(
        <TicketFilter
          model={BOARD_FILTER_MODEL}
          values={values(false, false, "")}
          onChange={onChange}
          sort={{ model: new SortModel(["newest"], "newest"), value: "newest", onChange: vi.fn() }}
        />,
      );
      expect(screen.queryByLabelText("Sort by")).toBeNull();
    });
  });
});

function Owned({ initial = APPROVALS_FILTER_MODEL.defaults() }: { initial?: FilterValues }) {
  const [current, setCurrent] = useState(initial);
  return <TicketFilter model={APPROVALS_FILTER_MODEL} values={current} onChange={setCurrent} />;
}

describe("TicketFilter on a phone", () => {
  beforeEach(() => stubNarrowLayout());

  function toggle(): HTMLElement {
    return screen.getByRole("button", { name: /^Filters/ });
  }

  it("keeps the fields hidden until the toggle is pressed, and names them by it", () => {
    render(<Owned />);

    expect(toggle()).toHaveAccessibleName("Filters");
    expect(toggle()).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("searchbox", { name: "Title or text" })).toBeNull();
    expect(document.getElementById(toggle().getAttribute("aria-controls")!)).toContainElement(
      screen.getByRole("searchbox", { name: "Title or text", hidden: true }),
    );

    toggle().focus();
    fireEvent.click(toggle());
    expect(toggle()).toHaveFocus();
    expect(toggle()).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("searchbox", { name: "Title or text" })).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Priority" })).toBeVisible();

    fireEvent.click(toggle());
    expect(toggle()).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("combobox", { name: "Priority", hidden: true })).not.toBeVisible();
  });

  it("shows how many fields are filtered, and starts closed with them applied", () => {
    render(<Owned initial={{ ...APPROVALS_FILTER_MODEL.defaults(), priority: "high", created: { from: "2026-10-01", to: "" } }} />);

    expect(toggle()).toHaveTextContent("Filters \u00b7 2");
    expect(toggle()).toHaveAttribute("aria-expanded", "false");
  });

  it("follows a change made in the open fields", () => {
    render(<Owned />);
    fireEvent.click(toggle());
    choose("Priority", "urgent");

    expect(toggle()).toHaveTextContent("Filters \u00b7 1");
    expect(toggle()).toHaveAttribute("aria-expanded", "true");
  });

  it("clears the filter from the row, leaving a plain Filters", () => {
    render(<Owned initial={{ ...APPROVALS_FILTER_MODEL.defaults(), priority: "high" }} />);

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(toggle()).toHaveAccessibleName("Filters");
    expect(toggle()).not.toHaveTextContent("\u00b7");
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  });
});

describe("TicketFilter on a wide window", () => {
  it("shows every field and has no toggle", () => {
    render(<Owned />);

    expect(screen.queryByRole("button", { name: /^Filters/ })).toBeNull();
    expect(screen.getByRole("searchbox", { name: "Title or text" })).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Priority" })).toBeVisible();
    expect(screen.getByLabelText("Created from")).toBeVisible();
  });
});
