import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Status } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { LoggerContext } from "@/context/logger";
import UnreviewedPanel from "./unreviewed_panel";
import { SORT_IDS, SORTS, type SortId } from "@/lib/ticket_query/ticket_sort";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import { DEFAULT_UNREVIEWED_VALUES, UNREVIEWED_FILTER_MODEL } from "@/domain/unreviewed_filter";
import { chooseFromDropdown } from "@/test/widgets";

const onFilterChange = vi.fn();
const logger = { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn(), event: vi.fn() };

beforeEach(() => {
  onFilterChange.mockReset();
});

interface PanelOptions {
  text?: string;
  from?: string;
  to?: string;
  excluded?: Status[];
}

function valuesFor({ text = "", from = "", to = "", excluded }: PanelOptions = {}): FilterValues {
  return {
    ...DEFAULT_UNREVIEWED_VALUES,
    text,
    closed: { from, to },
    ...(excluded && { status: excluded }),
  };
}

// Owns the values the way the route does, so a filter change reaches the list.
function Harness({ initial, initialSort = "recently_closed" }: { initial: FilterValues; initialSort?: SortId }) {
  const [values, setValues] = useState(initial);
  const [sort, setSort] = useState<SortId>(initialSort);
  return (
    <UnreviewedPanel
      values={values}
      sort={sort}
      onSortChange={setSort}
      onFilterChange={(next) => {
        onFilterChange(next);
        setValues(next);
      }}
    />
  );
}

function renderPanel(options: PanelOptions = {}, sort?: SortId) {
  return renderSignedIn(
    <LoggerContext.Provider value={logger}>
      <Harness initial={valuesFor(options)} initialSort={sort} />
    </LoggerContext.Provider>,
  );
}

function pageOf(tickets: ReturnType<typeof makeTicket>[], nextCursor?: string) {
  return { tickets, nextCursor };
}

const DEFAULT_STATUSES = FILTER_STATUSES.filter((s) => s !== "parked" && s !== "cancelled");

describe("UnreviewedPanel", () => {
  it("lists the tickets awaiting a post-merge review in the server's order, each with its PR", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([
      makeTicket({ id: 9, title: "Merged last", status: "done", closed_at: "2026-02-03T00:00:00Z", pr_url: "https://example.test/pull/9" }),
      makeTicket({ id: 4, title: "Merged first", status: "done", closed_at: "2026-02-01T00:00:00Z", pr_url: null }),
    ]));

    await renderPanel();

    const rows = await screen.findAllByRole("listitem");
    expect(list).toHaveBeenCalledWith({
      awaitingPostReview: true,
      statuses: DEFAULT_STATUSES,
      text: "",
      closedFrom: undefined,
      closedTo: undefined,
      cursor: undefined,
      sort: "recently_closed",
      limit: 50,
    });
    expect(rows.map((r) => within(r).getAllByRole("link")[0].textContent)).toEqual(["#9 Merged last", "#4 Merged first"]);
    expect(within(rows[0]).getByRole("link", { name: "PR for #9" })).toHaveAttribute("href", "https://example.test/pull/9");
    expect(within(rows[1]).getByText("no PR reported")).toBeInTheDocument();
    // Opened from here, the ticket's Back link returns here.
    expect(within(rows[0]).getAllByRole("link")[0].getAttribute("href")).toContain("from=unreviewed");
  });

  it("says so when there is nothing to review", async () => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel();

    expect(await screen.findByText(/Nothing to review/)).toBeInTheDocument();
  });

  it("shows the server's error when the list fails to load, and Try again asks again", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockRejectedValueOnce(new ApiError(500, "database is down"));
    const user = userEvent.setup();

    await renderPanel();
    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");

    list.mockResolvedValue(pageOf([makeTicket({ id: 3, title: "Back again", status: "done" })]));
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText(/Back again/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("asks the server for the filter it is given", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel({ text: "login", from: "2026-01-01", to: "2026-01-31" });

    await screen.findByText(/No tickets match/);
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({ text: "login", closedFrom: "2026-01-01", closedTo: "2026-01-31", cursor: undefined }),
    );
    expect(screen.getByLabelText("Title or text")).toHaveValue("login");
    expect(screen.getByLabelText("Merged from")).toHaveValue("2026-01-01");
  });

  it("loads more by the cursor the server returned, appends it, and stops when there is none", async () => {
    const list = vi
      .spyOn(apiClient, "listTicketsPage")
      .mockResolvedValueOnce(pageOf([makeTicket({ id: 3, title: "First page", status: "done" })], "next-1"))
      .mockResolvedValueOnce(pageOf([makeTicket({ id: 2, title: "Second page", status: "done" })]));
    const user = userEvent.setup();

    await renderPanel({ text: "x" });
    await user.click(await screen.findByRole("button", { name: "Load more" }));

    expect(await screen.findByText(/Second page/)).toBeInTheDocument();
    expect(screen.getByText(/First page/)).toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ text: "x", cursor: "next-1" }));
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /page/i })).not.toBeInTheDocument();
  });

  it("starts again from the first page when the filter changes", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockImplementation(async ({ text, cursor }) => {
      if (cursor) return pageOf([makeTicket({ id: 2, title: "Second page", status: "done" })]);
      return pageOf([makeTicket({ id: text ? 8 : 3, title: text ? "Filtered" : "First page", status: "done" })], text ? undefined : "next-1");
    });
    const user = userEvent.setup();

    await renderPanel();
    await user.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText(/Second page/);

    await user.type(screen.getByLabelText("Title or text"), "z");

    expect(await screen.findByText(/Filtered/)).toBeInTheDocument();
    expect(screen.queryByText(/Second page/)).not.toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ text: "z", cursor: undefined }));
  });

  it("applies a date as it is picked, with the other filters kept", async () => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel({ text: "login" });
    await screen.findByText(/No tickets match/);
    fireEvent.change(screen.getByLabelText("Merged from"), { target: { value: "2026-02-01" } });

    expect(onFilterChange).toHaveBeenLastCalledWith({
      ...DEFAULT_UNREVIEWED_VALUES,
      text: "login",
      closed: { from: "2026-02-01", to: "" },
    });
    expect(screen.getByRole("button", { name: "Clear filters" })).toBeInTheDocument();
  });

  it("Clear filters drops every filter", async () => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));
    const user = userEvent.setup();

    await renderPanel({ text: "a", to: "2026-03-01", excluded: ["open"] });
    await user.click(await screen.findByRole("button", { name: "Clear filters" }));

    expect(onFilterChange).toHaveBeenCalledExactlyOnceWith(DEFAULT_UNREVIEWED_VALUES);
  });

  it("hides parked and cancelled by default", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel();
    await screen.findByText(/Nothing to review/);

    expect(screen.getByRole("checkbox", { name: "Parked" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Cancelled" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Done" })).toBeChecked();
    expect(list.mock.calls[0][0].statuses).not.toContain("parked");
  });

  it("asks the server for the statuses left once the default is changed", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel({ excluded: ["parked", "cancelled", "open"] });
    await screen.findByText(/No tickets match/);

    expect(list.mock.calls[0][0].statuses).toEqual(FILTER_STATUSES.filter((s) => !["parked", "cancelled", "open"].includes(s)));
  });

  it("shows nothing, without asking, when every status is unticked", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel({ excluded: [...FILTER_STATUSES] });

    await screen.findByText(/No tickets match/);
    expect(list).not.toHaveBeenCalled();
  });

  it("round-trips the filter through the address, leaving defaults out", () => {
    const values = { ...DEFAULT_UNREVIEWED_VALUES, text: "a b", closed: { from: "2026-01-01", to: "" } };
    expect(UNREVIEWED_FILTER_MODEL.toSearch(values)).toMatchObject({ q: "a b", from: "2026-01-01", to: undefined, exclude: undefined });
    expect(UNREVIEWED_FILTER_MODEL.fromSearch(UNREVIEWED_FILTER_MODEL.toSearch(values))).toEqual(values);
    expect(UNREVIEWED_FILTER_MODEL.isFiltered(DEFAULT_UNREVIEWED_VALUES)).toBe(false);
  });

  it("sorts by the person's choice, from the first page, keeping the filter and not calling it filtering", async () => {
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([makeTicket({ id: 9, title: "Nine", status: "done" })], "c2"));
    const user = userEvent.setup();
    await renderPanel({ text: "login" });
    await screen.findByText("Nine");
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "recently_closed", cursor: "c2" }));

    list.mockResolvedValue(pageOf([]));
    await chooseFromDropdown(user, "Sort by", "Priority (urgent first)");

    await vi.waitFor(() => expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "priority", text: "login", cursor: undefined })));
    expect(onFilterChange).not.toHaveBeenCalled();
  });

  it("calls an empty list under a non-default sort empty, not filtered", async () => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));

    await renderPanel({}, "oldest");

    expect(await screen.findByText(/Nothing to review/)).toBeInTheDocument();
    expect(screen.queryByText("No tickets match these filters.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  });

  it("offers every sort, with the merged one first", async () => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue(pageOf([]));
    await renderPanel();
    const select = await screen.findByRole("combobox", { name: "Sort by" });
    expect(select).toHaveTextContent("Recently closed or merged");
    await userEvent.click(select);
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(SORT_IDS.map((id) => SORTS[id].label));
  });
});
