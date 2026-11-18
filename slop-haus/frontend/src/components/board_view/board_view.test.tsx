import type { SortId } from "@/lib/ticket_query/ticket_sort";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  apiClient,
  ApiError,
  BulkRefusedError,
  type BoardChange,
  type Status,
  type Ticket,
} from "@/api";
import { makeAttemptCard, makeTicket } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { LoggerContext } from "@/context/logger";
import { chooseFromDropdown } from "@/test/widgets";
import { BOARD_FILTER_MODEL } from "@/domain/board_filter";
import { TEXT_DEBOUNCE_MS } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import BoardView from "./board_view";
import { LIVE_REFRESH_THROTTLE_MS } from "@/lib/live";
import { serveTickets, type Page } from "@/test/serve_tickets";

const LIVE: Status[] = [
  "preparing",
  "open",
  "in_progress",
  "in_merge_queue",
  "blocked",
  "discussion",
  "review",
];

const logger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  event: vi.fn(),
};

interface Initial {
  done?: boolean;
  parked?: boolean;
  text?: string;
}

// The filter is BoardView's caller's (BoardRoute keeps it in the address);
// this stands in for that caller.
function FilteredBoardView({ initial = {} }: { initial?: Initial }) {
  const [values, setValues] = useState<FilterValues>(() => ({
    ...BOARD_FILTER_MODEL.defaults(),
    text: initial.text ?? "",
    done: initial.done ?? false,
    parked: initial.parked ?? false,
  }));
  const [sort, setSort] = useState<SortId>("newest");
  return (
    <LoggerContext.Provider value={logger}>
      <BoardView
        filterValues={values}
        onFilterChange={setValues}
        sort={sort}
        onSortChange={setSort}
      />
    </LoggerContext.Provider>
  );
}

function column(name: string): HTMLElement {
  return screen
    .getByRole("heading", { name })
    .closest(".board__column") as HTMLElement;
}

type Filter = Parameters<typeof apiClient.listTicketsPage>[0];

const serve = serveTickets;

function requests(): Filter[] {
  return vi
    .mocked(apiClient.listTicketsPage)
    .mock.calls.map(([filter]) => filter);
}

function requestedStatuses(): Status[][] {
  return requests().map((r) => r.statuses ?? []);
}

// A work column's first page is the board's; these tests serve every page
// from the one tickets mock, as the server does for tickets with one attempt.
beforeEach(() => {
  vi.spyOn(apiClient, "listBoardColumn").mockImplementation((filter) =>
    apiClient.listTicketsPage(filter),
  );
});

describe("BoardView", () => {
  it("has no ticket-drafting launcher: that is a Remote Control session now", async () => {
    serve(() => []);
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([]);
    const conductors = vi.spyOn(apiClient, "listConductors");

    await renderSignedIn(<FilteredBoardView />);

    await screen.findByRole("heading", { name: "Open" });
    expect(screen.queryByText(/Draft a ticket/)).not.toBeInTheDocument();
    expect(conductors).not.toHaveBeenCalled();
  });

  // The point of showing the lock on the board: visible without opening a
  // ticket.
  it("shows who holds the integration lock and who is queued, above the columns", async () => {
    serve(() => []);
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      {
        name: "integration",
        holder: {
          agent_id: "11111111-1111-4111-8111-111111111111",
          agent_name: "merger",
          ticket_id: 7,
          ticket_title: "Ship it",
          acquired_at: "2026-10-03T15:00:00Z",
          held_seconds: 600,
          last_heartbeat_at: "2026-10-03T15:19:00Z",
          since_heartbeat_seconds: 60,
          dead: false,
          long_held: false,
        },
        queue: [
          {
            agent_id: "22222222-2222-4222-8222-222222222222",
            agent_name: "waiting-agent",
            ticket_id: 8,
            ticket_title: "Next",
            queued_at: "2026-10-03T15:05:00Z",
            waited_seconds: 300,
            priority: "normal",
          },
        ],
      },
    ]);

    await renderSignedIn(<FilteredBoardView />);

    const panel = await screen.findByRole("region", {
      name: "Integration lock",
    });
    expect(within(panel).getByText("merger")).toBeInTheDocument();
    expect(within(panel).getByText("waiting-agent")).toBeInTheDocument();
  });

  it("reloads the lists after a ticket is filed, so the new ticket shows", async () => {
    const user = userEvent.setup();
    const all: Ticket[] = [];
    serve(() => all);
    vi.spyOn(apiClient, "createTicket").mockImplementation(async () => {
      all.push(makeTicket({ id: 7, title: "Fresh ticket", status: "open" }));
      return all[0];
    });

    await renderSignedIn(<FilteredBoardView />);
    await user.click(screen.getByRole("button", { name: "File a ticket" }));
    await screen.findByRole("heading", { name: "Open" });

    await user.type(screen.getByLabelText("Title"), "Fresh ticket");
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(
      await within(column("Open")).findByText("Fresh ticket"),
    ).toBeInTheDocument();
    // The refresh must not remount the form and fold it back up.
    expect(
      screen.getByRole("button", { name: "File a ticket" }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  // The mount fetch and the reload after filing can both be in flight; if
  // the older one resolves last it must not put back a list without the
  // new ticket.
  it("keeps the newer list when an older request resolves after it", async () => {
    const user = userEvent.setup();
    let resolveMount!: (page: Page) => void;
    let open = 0;
    vi.spyOn(apiClient, "listTicketsPage").mockImplementation(
      (filter: Filter) => {
        if (filter.statuses?.[0] !== "open")
          return Promise.resolve({ tickets: [] });
        return ++open === 1
          ? new Promise((resolve) => (resolveMount = resolve))
          : Promise.resolve({
              tickets: [
                makeTicket({ id: 7, title: "Fresh ticket", status: "open" }),
              ],
            });
      },
    );
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(
      makeTicket({ id: 7 }),
    );

    await renderSignedIn(<FilteredBoardView />);
    await user.click(screen.getByRole("button", { name: "File a ticket" }));
    await user.type(screen.getByLabelText("Title"), "Fresh ticket");
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(
      await within(column("Open")).findByText("Fresh ticket"),
    ).toBeInTheDocument();

    await act(async () => resolveMount({ tickets: [] }));

    expect(
      within(column("Open")).getByText("Fresh ticket"),
    ).toBeInTheDocument();
  });

  it("shows the server's error on the column that failed, and Try again asks for that column", async () => {
    const user = userEvent.setup();
    const list = serve(() => [
      makeTicket({ id: 1, title: "Blocked one", status: "blocked" }),
    ]);
    const working = list.getMockImplementation()!;
    list.mockImplementation(async (filter: Filter) => {
      if (filter.statuses?.[0] === "review")
        throw new ApiError(500, "database is down");
      return working(filter);
    });

    await renderSignedIn(<FilteredBoardView />);

    expect(
      await within(column("Review")).findByRole("alert"),
    ).toHaveTextContent("database is down");
    expect(
      await within(column("Blocked")).findByText("Blocked one"),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("alert")).toHaveLength(1);

    list.mockImplementation(async (filter: Filter) => {
      if (filter.statuses?.[0] === "review")
        return {
          tickets: [makeTicket({ id: 2, title: "Reviewed", status: "review" })],
        };
      return working(filter);
    });
    const before = requests().length;
    await user.click(
      within(column("Review")).getByRole("button", { name: "Try again" }),
    );

    expect(
      await within(column("Review")).findByText("Reviewed"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(requests().slice(before)).toEqual([
      {
        statuses: ["review"],
        text: undefined,
        cursor: undefined,
        sort: "newest",
        limit: 200,
      },
    ]);
  });

  // What the board asks the server for comes from the filter model's query,
  // one request per column: the live columns, and Done and Parked only when
  // the filter adds them.
  describe("the statuses it asks for", () => {
    it.each([
      [{}, LIVE],
      [{ done: true }, [...LIVE, "done"]],
      [{ parked: true }, ["parked", ...LIVE]],
      [{ done: true, parked: true }, ["parked", ...LIVE, "done"]],
    ] as [Initial, Status[]][])("with %j", async (initial, columns) => {
      serve(() => []);

      await renderSignedIn(<FilteredBoardView initial={initial} />);

      await waitFor(() => expect(requests()).toHaveLength(columns.length));
      expect(
        requestedStatuses()
          .map((s) => s[0])
          .sort(),
      ).toEqual([...columns].sort());
      expect(requestedStatuses().every((s) => s.length === 1)).toBe(true);
      expect(
        [...document.querySelectorAll(".board__column h2")].map(
          (h) => h.textContent,
        ),
      ).toEqual(columns.filter((s) => s !== "in_merge_queue").map((s) => headingOf(s)));
    });

    describe("the In merge queue column", () => {
      const mode = (merge_method: "worker" | "merge_queue") =>
        vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue({
          merge_method,
          merge_queue_enabled: true,
          change_tools: true,
          merge_queue_available: true,
        });

      it("is drawn when the tenant lands by the merge queue, even empty", async () => {
        mode("merge_queue");
        serve(() => []);
        await renderSignedIn(<FilteredBoardView />);
        expect(await screen.findByRole("heading", { name: "In merge queue" })).toBeInTheDocument();
      });

      it("is left off in worker mode while nothing is in it", async () => {
        const caps = mode("worker");
        serve(() => []);
        await renderSignedIn(<FilteredBoardView />);
        await screen.findByRole("heading", { name: "Open" });
        await waitFor(() => expect(caps).toHaveBeenCalled());
        expect(screen.queryByRole("heading", { name: "In merge queue" })).not.toBeInTheDocument();
      });

      it("stays in worker mode while a ticket is still in it, so a switch does not hide the queue", async () => {
        mode("worker");
        serve(() => [makeTicket({ id: 9, title: "Queued", status: "in_merge_queue" })]);
        await renderSignedIn(<FilteredBoardView />);
        const heading = await screen.findByRole("heading", { name: "In merge queue" });
        expect(await within(heading.closest(".board__column") as HTMLElement).findByText("Queued")).toBeInTheDocument();
      });
    });

    it("leaves Done and Parked off the board and unrequested until they are ticked", async () => {
      serve(() => []);

      await renderSignedIn(<FilteredBoardView />);

      await screen.findByRole("heading", { name: "Open" });
      expect(
        screen.getByRole("checkbox", { name: "Include done" }),
      ).not.toBeChecked();
      expect(
        screen.queryByRole("heading", { name: "Done" }),
      ).not.toBeInTheDocument();
      expect(requestedStatuses().flat()).not.toContain("done");
      expect(requestedStatuses().flat()).not.toContain("parked");
    });
  });

  it("fetches only the Done column when Include done is ticked, leaving the others alone", async () => {
    const user = userEvent.setup();
    serve(() => [
      makeTicket({ id: 1, title: "Live ticket", status: "open" }),
      makeTicket({ id: 2, title: "Finished ticket", status: "done" }),
    ]);

    await renderSignedIn(<FilteredBoardView />);
    await screen.findByText("Live ticket");
    const before = requests().length;
    await user.click(screen.getByRole("checkbox", { name: "Include done" }));

    expect(
      await within(column("Done")).findByText("Finished ticket"),
    ).toBeInTheDocument();
    expect(requestedStatuses().slice(before)).toEqual([["done"]]);
    expect(within(column("Open")).getByText("Live ticket")).toBeInTheDocument();
  });

  it("says the Done column is loading, not empty, until its first page lands", async () => {
    const user = userEvent.setup();
    let resolveDone!: (page: Page) => void;
    vi.spyOn(apiClient, "listTicketsPage").mockImplementation(
      (filter: Filter) =>
        filter.statuses?.[0] === "done"
          ? new Promise((resolve) => (resolveDone = resolve))
          : Promise.resolve({ tickets: [] }),
    );

    await renderSignedIn(<FilteredBoardView />);
    await screen.findByRole("heading", { name: "Open" });
    await user.click(screen.getByRole("checkbox", { name: "Include done" }));

    expect(within(column("Done")).getByText("Loading…")).toBeInTheDocument();
    expect(
      within(column("Done")).queryByText("No tickets"),
    ).not.toBeInTheDocument();

    await act(async () =>
      resolveDone({
        tickets: [
          makeTicket({ id: 2, title: "Finished ticket", status: "done" }),
        ],
      }),
    );

    expect(
      within(column("Done")).getByText("Finished ticket"),
    ).toBeInTheDocument();
  });

  it("drops the Done column again when Include done is unticked, without asking for anything", async () => {
    const user = userEvent.setup();
    serve(() => [
      makeTicket({ id: 2, title: "Finished ticket", status: "done" }),
    ]);

    await renderSignedIn(<FilteredBoardView />);
    await screen.findByRole("heading", { name: "Open" });
    const includeDone = screen.getByRole("checkbox", { name: "Include done" });
    await user.click(includeDone);
    await within(column("Done")).findByText("Finished ticket");
    const before = requests().length;

    await user.click(includeDone);

    await waitFor(() =>
      expect(
        screen.queryByRole("heading", { name: "Done" }),
      ).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Finished ticket")).not.toBeInTheDocument();
    expect(requests()).toHaveLength(before);
  });

  // Parked tickets are hidden until asked for; asked for, they are fetched
  // -- and searched, so an old idea is found before it is filed again.
  it("shows parked tickets only with Include parked, searching them too", async () => {
    const user = userEvent.setup();
    serve(() => [
      makeTicket({ id: 3, title: "Zebra someday", status: "parked" }),
    ]);

    await renderSignedIn(<FilteredBoardView initial={{ text: "zebra" }} />);
    await screen.findByRole("heading", { name: "Open" });
    expect(
      screen.queryByRole("heading", { name: "Parked" }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: "Include parked" }));

    expect(
      await within(
        await screen
          .findByRole("heading", { name: "Parked" })
          .then(() => column("Parked")),
      ).findByText("Zebra someday"),
    ).toBeInTheDocument();
    expect(requests().at(-1)).toEqual({
      statuses: ["parked"],
      text: "zebra",
      cursor: undefined,
      sort: "newest",
      limit: 50,
    });
  });

  it("asks for the largest page for the live columns, and the default for Done and Parked", async () => {
    serve(() => []);
    await renderSignedIn(
      <FilteredBoardView initial={{ done: true, parked: true }} />,
    );
    await screen.findByRole("heading", { name: "Done" });

    const limits = new Map(requests().map((r) => [r.statuses![0], r.limit]));
    expect(limits.get("open")).toBe(200);
    expect(limits.get("in_progress")).toBe(200);
    expect(limits.get("done")).toBe(50);
    expect(limits.get("parked")).toBe(50);
  });

  it("applies one sort to every column, from the first page", async () => {
    const user = userEvent.setup();
    serve(() => [makeTicket({ id: 3, title: "A ticket", status: "open" })]);
    await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
    await screen.findByRole("heading", { name: "Done" });
    const columns = new Set(requests().map((r) => r.statuses![0]));
    const before = requests().length;

    await chooseFromDropdown(user, "Sort by", "Priority (urgent first)");

    await waitFor(() =>
      expect(
        new Set(
          requests()
            .slice(before)
            .map((r) => r.statuses![0]),
        ),
      ).toEqual(columns),
    );
    expect(
      requests()
        .slice(before)
        .every((r) => r.sort === "priority" && r.cursor === undefined),
    ).toBe(true);
  });

  // Each of Done and Parked is its own list, with its own cursor and its own
  // "Load more": paging one must not move the other.
  describe("paging a column", () => {
    // A server that pages every status two at a time, by the cursor "<status>:<offset>".
    function servePaged(all: Ticket[]) {
      return vi
        .spyOn(apiClient, "listTicketsPage")
        .mockImplementation(async (filter: Filter): Promise<Page> => {
          const status = filter.statuses![0];
          const start = filter.cursor ? Number(filter.cursor.split(":")[1]) : 0;
          const matching = all.filter((t) => t.status === status);
          const next = start + 2;
          return {
            tickets: matching.slice(start, next),
            nextCursor:
              next < matching.length ? `${status}:${next}` : undefined,
          };
        });
    }
    const done = [3, 2, 1].map((id) =>
      makeTicket({ id, title: `Done ${id}`, status: "done" }),
    );
    const parked = [13, 12, 11].map((id) =>
      makeTicket({ id, title: `Parked ${id}`, status: "parked" }),
    );

    it("loads more of the Done column by its own cursor, and not of Parked", async () => {
      const user = userEvent.setup();
      servePaged([...done, ...parked]);

      await renderSignedIn(
        <FilteredBoardView initial={{ done: true, parked: true }} />,
      );
      await within(column("Done")).findByText("Done 3");
      await within(column("Parked")).findByText("Parked 13");
      expect(
        within(column("Done")).queryByText("Done 1"),
      ).not.toBeInTheDocument();
      expect(within(column("Done")).getByText("2+")).toBeInTheDocument();

      await user.click(
        within(column("Done")).getByRole("button", { name: "Load more" }),
      );

      expect(
        await within(column("Done")).findByText("Done 1"),
      ).toBeInTheDocument();
      expect(requests().at(-1)).toEqual({
        statuses: ["done"],
        text: undefined,
        cursor: "done:2",
        sort: "newest",
        limit: 50,
      });
      expect(
        within(column("Done")).queryByRole("button", { name: "Load more" }),
      ).not.toBeInTheDocument();
      expect(
        within(column("Parked")).queryByText("Parked 11"),
      ).not.toBeInTheDocument();
      expect(
        within(column("Parked")).getByRole("button", { name: "Load more" }),
      ).toBeInTheDocument();
    });

    it("keeps the cards a column has when its Load more fails, and Try again asks for the same cursor", async () => {
      const user = userEvent.setup();
      const list = servePaged(done);
      const working = list.getMockImplementation()!;
      await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
      await within(column("Done")).findByText("Done 3");
      list.mockRejectedValueOnce(new ApiError(503, "unavailable"));

      await user.click(
        within(column("Done")).getByRole("button", { name: "Load more" }),
      );

      expect(
        await within(column("Done")).findByRole("alert"),
      ).toHaveTextContent("unavailable");
      expect(within(column("Done")).getByText("Done 3")).toBeInTheDocument();
      list.mockImplementation(working);
      await user.click(
        within(column("Done")).getByRole("button", { name: "Try again" }),
      );

      expect(
        await within(column("Done")).findByText("Done 1"),
      ).toBeInTheDocument();
      expect(requests().at(-1)).toEqual({
        statuses: ["done"],
        text: undefined,
        cursor: "done:2",
        sort: "newest",
        limit: 50,
      });
    });

    it("loads more of the Parked column by its own cursor, and not of Done", async () => {
      const user = userEvent.setup();
      servePaged([...done, ...parked]);

      await renderSignedIn(
        <FilteredBoardView initial={{ done: true, parked: true }} />,
      );
      await within(column("Parked")).findByText("Parked 13");

      await user.click(
        within(column("Parked")).getByRole("button", { name: "Load more" }),
      );

      expect(
        await within(column("Parked")).findByText("Parked 11"),
      ).toBeInTheDocument();
      expect(requests().at(-1)).toEqual({
        statuses: ["parked"],
        text: undefined,
        cursor: "parked:2",
        sort: "newest",
        limit: 50,
      });
      expect(
        within(column("Done")).queryByText("Done 1"),
      ).not.toBeInTheDocument();
    });

    it("pages a live column too", async () => {
      const user = userEvent.setup();
      servePaged(
        [1, 2, 3].map((id) =>
          makeTicket({ id, title: `Open ${id}`, status: "open" }),
        ),
      );

      await renderSignedIn(<FilteredBoardView />);
      await within(column("Open")).findByText("Open 1");
      await user.click(
        within(column("Open")).getByRole("button", { name: "Load more" }),
      );

      expect(
        await within(column("Open")).findByText("Open 3"),
      ).toBeInTheDocument();
      expect(requests().at(-1)?.cursor).toBe("open:2");
    });

    // A new filter is a new list: what was paged in is gone, and so are the
    // cursors.
    it("starts every column again from the first page when the search changes", async () => {
      const user = userEvent.setup();
      servePaged([
        ...done,
        ...parked,
        ...[1, 2, 3].map((id) =>
          makeTicket({ id: 20 + id, title: `Open ${id}`, status: "open" }),
        ),
      ]);

      await renderSignedIn(
        <FilteredBoardView initial={{ done: true, parked: true }} />,
      );
      await within(column("Done")).findByText("Done 3");
      await user.click(
        within(column("Done")).getByRole("button", { name: "Load more" }),
      );
      await within(column("Done")).findByText("Done 1");
      const before = requests().length;

      await user.type(
        screen.getByRole("searchbox", { name: "Search tickets" }),
        "x",
      );

      await waitFor(() => expect(requests().length).toBe(before + 9));
      const after = requests().slice(before);
      expect(after.every((r) => r.cursor === undefined && r.text === "x")).toBe(
        true,
      );
      expect(after.map((r) => r.statuses![0]).sort()).toEqual(
        ["parked", ...LIVE, "done"].sort(),
      );
    });

    it("keeps what a column has paged in while another column's filter flag changes", async () => {
      const user = userEvent.setup();
      servePaged([...done, ...parked]);

      await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
      await within(column("Done")).findByText("Done 3");
      await user.click(
        within(column("Done")).getByRole("button", { name: "Load more" }),
      );
      await within(column("Done")).findByText("Done 1");
      const before = requests().length;

      await user.click(
        screen.getByRole("checkbox", { name: "Include parked" }),
      );

      await within(column("Parked")).findByText("Parked 13");
      expect(requests().slice(before)).toEqual([
        {
          statuses: ["parked"],
          text: undefined,
          cursor: undefined,
          sort: "newest",
          limit: 50,
        },
      ]);
      expect(within(column("Done")).getByText("Done 1")).toBeInTheDocument();
    });
  });

  // Ticking the box while a request is still out: that request's list is for
  // the old filter, so letting it land would show a board that disagrees
  // with the filter.
  it("ignores a request for the old search that resolves after the search changed", async () => {
    const user = userEvent.setup();
    let resolveStale!: (page: Page) => void;
    vi.spyOn(apiClient, "listTicketsPage").mockImplementation(
      (filter: Filter) => {
        if (filter.statuses?.[0] !== "open")
          return Promise.resolve({ tickets: [] });
        return filter.text
          ? Promise.resolve({
              tickets: [
                makeTicket({ id: 2, title: "Zebra ticket", status: "open" }),
              ],
            })
          : new Promise((resolve) => (resolveStale = resolve));
      },
    );

    await renderSignedIn(<FilteredBoardView />);
    await user.type(
      screen.getByRole("searchbox", { name: "Search tickets" }),
      "zebra",
    );
    expect(
      await within(column("Open")).findByText("Zebra ticket"),
    ).toBeInTheDocument();
    await act(async () =>
      resolveStale({
        tickets: [makeTicket({ id: 1, title: "Stale list", status: "open" })],
      }),
    );

    expect(screen.queryByText("Stale list")).not.toBeInTheDocument();
    expect(
      within(column("Open")).getByText("Zebra ticket"),
    ).toBeInTheDocument();
  });

  describe("the text filter", () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    it("asks once typing pauses, not once per letter, keeping the done filter", async () => {
      serve(() => [
        makeTicket({ id: 1, title: "Everything", status: "open" }),
        makeTicket({ id: 2, title: "Zebra import", status: "open" }),
      ]);

      await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
      await screen.findByText("Everything");
      const before = requests().length;
      // Faked only while typing: the finds around it poll on real timers.
      // fireEvent because user-event's async wrapper waits on a real
      // setTimeout(0), which never fires under fake timers.
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
      const box = screen.getByRole("searchbox", { name: "Search tickets" });
      for (const typed of ["z", "ze", "zeb", "zebr", "zebra"]) {
        fireEvent.change(box, { target: { value: typed } });
        act(() => vi.advanceTimersByTime(TEXT_DEBOUNCE_MS - 1));
      }

      expect(requests()).toHaveLength(before);

      act(() => vi.advanceTimersByTime(1));
      vi.useRealTimers();

      expect(requests()).toHaveLength(before + LIVE.length + 1);
      expect(
        requests()
          .slice(before)
          .every((r) => r.text === "zebra"),
      ).toBe(true);
      expect(requestedStatuses().slice(before).flat()).toContain("done");
      expect(
        await within(column("Open")).findByText("Zebra import"),
      ).toBeInTheDocument();
      expect(
        within(column("Open")).queryByText("Everything"),
      ).not.toBeInTheDocument();
    });

    it("starts from the filter it is given", async () => {
      serve(() => []);

      await renderSignedIn(<FilteredBoardView initial={{ text: "zebra" }} />);

      await screen.findByRole("heading", { name: "Open" });
      await waitFor(() => expect(requests()).toHaveLength(LIVE.length));
      expect(requests().every((r) => r.text === "zebra")).toBe(true);
      expect(
        screen.getByRole("searchbox", { name: "Search tickets" }),
      ).toHaveValue("zebra");
    });
  });

  // Kept current while it stays mounted: on the stream's changes, on a
  // return to the tab and on a restore from the back/forward cache. The
  // hook's own timing and stream handling are use_live_refresh.test.ts's;
  // these are about what a reload does to the board on screen.
  describe("staying current", () => {
    // A stream event: a bare revision is one the board must reload for, as a
    // server from before the stream named tickets sends it.
    let changed: (revision: number, change?: Partial<BoardChange>) => void;

    beforeEach(() => {
      vi.spyOn(apiClient, "streamBoardChanges").mockImplementation(
        (onChange) => {
          changed = (revision, change) =>
            onChange(revision, { revision, ...change });
          return vi.fn();
        },
      );
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    // fire, then run the throttled reload it asked for. The throttle's
    // window opens with the board's last reload, so the next is due at the
    // window's end -- up to two windows on, when that was itself a reload
    // run by this, on a faked clock ahead of the real one. Faked only for
    // that, as the finds around it poll on real timers.
    function reloadAfter(fire: () => void) {
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
      act(fire);
      act(() => vi.advanceTimersByTime(2 * LIVE_REFRESH_THROTTLE_MS));
      vi.useRealTimers();
    }

    const boardChanged = () => reloadAfter(() => changed(2));

    it("reloads every column, with the filter it has, when the stream says the board changed", async () => {
      const all = [makeTicket({ id: 1, title: "Before", status: "open" })];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
      await screen.findByText("Before");
      const before = requests().length;

      all[0] = makeTicket({ id: 1, title: "After", status: "in_progress" });
      boardChanged();

      expect(
        await within(column("In progress")).findByText("After"),
      ).toBeInTheDocument();
      expect(
        within(column("Open")).queryByText("Before"),
      ).not.toBeInTheDocument();
      expect(
        requestedStatuses()
          .slice(before)
          .map((s) => s[0])
          .sort(),
      ).toEqual([...LIVE, "done"].sort());
    });

    it("moves a ticket the stream names between columns without a request", async () => {
      const all = [
        makeTicket({ id: 1, title: "Mover", status: "open" }),
        makeTicket({ id: 2, title: "Stays", status: "open" }),
      ];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Mover");
      const before = requests().length;

      reloadAfter(() =>
        changed(2, {
          tickets: [
            makeTicket({ id: 1, title: "Mover", status: "in_progress" }),
          ],
        }),
      );

      expect(
        await within(column("In progress")).findByText("Mover"),
      ).toBeInTheDocument();
      expect(
        within(column("Open")).queryByText("Mover"),
      ).not.toBeInTheDocument();
      expect(within(column("Open")).getByText("Stays")).toBeInTheDocument();
      expect(requests()).toHaveLength(before);
    });

    it("keeps the pages a column has loaded when the stream names a ticket", async () => {
      const user = userEvent.setup();
      const done = [3, 2, 1].map((id) =>
        makeTicket({ id, title: `Done ${id}`, status: "done" }),
      );
      const open = makeTicket({ id: 9, title: "Open one", status: "open" });
      vi.spyOn(apiClient, "listTicketsPage").mockImplementation(
        async (filter: Filter): Promise<Page> => {
          if (filter.statuses![0] === "open") return { tickets: [open] };
          if (filter.statuses![0] !== "done") return { tickets: [] };
          return filter.cursor
            ? { tickets: done.slice(2) }
            : { tickets: done.slice(0, 2), nextCursor: "done:2" };
        },
      );
      await renderSignedIn(<FilteredBoardView initial={{ done: true }} />);
      await user.click(
        await within(column("Done")).findByRole("button", {
          name: "Load more",
        }),
      );
      await within(column("Done")).findByText("Done 1");
      const before = requests().length;

      reloadAfter(() =>
        changed(2, {
          tickets: [
            makeTicket({
              ...open,
              title: "Renamed",
              updated_at: "2099-01-01T00:00:00Z",
            }),
          ],
        }),
      );

      expect(
        await within(column("Open")).findByText("Renamed"),
      ).toBeInTheDocument();
      expect(within(column("Done")).getByText("Done 1")).toBeInTheDocument();
      expect(requests()).toHaveLength(before);
    });

    it("reloads every column when the stream says it cannot name the change", async () => {
      const all = [makeTicket({ id: 1, title: "Before", status: "open" })];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Before");
      const before = requests().length;

      all[0] = makeTicket({ id: 1, title: "After", status: "open" });
      reloadAfter(() => changed(2, { reload: true }));

      expect(await screen.findByText("After")).toBeInTheDocument();
      expect(requests().length).toBeGreaterThan(before);
    });

    it("reloads on a return to the tab, keeping the board up while it does", async () => {
      const all = [makeTicket({ id: 1, title: "Before", status: "open" })];
      let resolveReload!: (page: Page) => void;
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Before");
      vi.mocked(apiClient.listTicketsPage).mockImplementation(
        (filter: Filter) =>
          filter.statuses?.[0] === "open"
            ? new Promise((resolve) => (resolveReload = resolve))
            : Promise.resolve({ tickets: [] }),
      );

      reloadAfter(() => document.dispatchEvent(new Event("visibilitychange")));

      expect(screen.getByText("Before")).toBeInTheDocument();
      await act(async () =>
        resolveReload({
          tickets: [makeTicket({ id: 1, title: "After", status: "open" })],
        }),
      );
      expect(screen.getByText("After")).toBeInTheDocument();
    });

    it("reloads on a restore from the back/forward cache", async () => {
      const all = [makeTicket({ id: 1, title: "Before", status: "open" })];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Before");

      all[0] = makeTicket({ id: 1, title: "After", status: "open" });
      reloadAfter(() =>
        window.dispatchEvent(
          new PageTransitionEvent("pageshow", { persisted: true }),
        ),
      );

      expect(await screen.findByText("After")).toBeInTheDocument();
    });

    // Nobody asked for this reload, so its failure leaves what the column
    // has on screen, and says so on the column; the next change tries again.
    it("keeps the cards when a reload fails, says so on the column, and reloads on the next change", async () => {
      const all = [makeTicket({ id: 1, title: "Still here", status: "open" })];
      const list = serve(() => all);
      const working = list.getMockImplementation()!;
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Still here");
      list.mockImplementation(async () => {
        throw new ApiError(503, "unavailable");
      });

      boardChanged();

      expect(
        await within(column("Open")).findByRole("alert"),
      ).toHaveTextContent("unavailable");
      expect(screen.getByText("Still here")).toBeInTheDocument();

      all[0] = makeTicket({ id: 1, title: "Back again", status: "open" });
      list.mockImplementation(working);
      boardChanged();

      expect(await screen.findByText("Back again")).toBeInTheDocument();
      await waitFor(() =>
        expect(screen.queryByRole("alert")).not.toBeInTheDocument(),
      );
    });

    it("leaves a search being typed alone", async () => {
      serve(() => [makeTicket({ id: 1, title: "Card", status: "open" })]);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Card");
      const before = requests().length;
      const box = screen.getByRole("searchbox", { name: "Search tickets" });
      fireEvent.change(box, { target: { value: "half-typ" } });

      boardChanged();

      await waitFor(() => expect(requests().length).toBeGreaterThan(before));
      expect(
        screen.getByRole("searchbox", { name: "Search tickets" }),
      ).toHaveValue("half-typ");
    });

    // A slow reload the stream asked for must not land over the list for a
    // search chosen since.
    it("ignores a reload that resolves after a newer request for a changed search", async () => {
      const user = userEvent.setup();
      let stall = false;
      let resolveStale!: (page: Page) => void;
      vi.spyOn(apiClient, "listTicketsPage").mockImplementation(
        (filter: Filter) => {
          if (filter.statuses?.[0] !== "open")
            return Promise.resolve({ tickets: [] });
          if (filter.text)
            return Promise.resolve({
              tickets: [
                makeTicket({ id: 2, title: "Zebra one", status: "open" }),
              ],
            });
          if (stall) return new Promise((resolve) => (resolveStale = resolve));
          return Promise.resolve({
            tickets: [makeTicket({ id: 1, title: "Open one", status: "open" })],
          });
        },
      );
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Open one");

      stall = true;
      boardChanged();
      await user.type(
        screen.getByRole("searchbox", { name: "Search tickets" }),
        "zebra",
      );
      expect(
        await within(column("Open")).findByText("Zebra one"),
      ).toBeInTheDocument();

      await act(async () =>
        resolveStale({
          tickets: [makeTicket({ id: 1, title: "Open one", status: "open" })],
        }),
      );

      expect(within(column("Open")).getByText("Zebra one")).toBeInTheDocument();
      expect(screen.queryByText("Open one")).not.toBeInTheDocument();
    });

    // A ticket that moves column stays selected -- it can still be acted
    // on -- and one that leaves the board is dropped from the selection.
    it("keeps the selection across a reload, less what left the board", async () => {
      const user = userEvent.setup();
      let all = [
        makeTicket({ id: 1, title: "Moves", status: "open" }),
        makeTicket({ id: 2, title: "Leaves", status: "open" }),
      ];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await user.click(await screen.findByRole("button", { name: "Select" }));
      await user.click(await screen.findByText("Moves"));
      await user.click(screen.getByText("Leaves"));
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("2 selected");

      all = [makeTicket({ id: 1, title: "Moves", status: "in_progress" })];
      boardChanged();

      const moved = await within(column("In progress")).findByText("Moves");
      expect(moved.closest(".ticket-card__select")).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      await waitFor(() =>
        expect(screen.queryByText("Leaves")).not.toBeInTheDocument(),
      );
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("1 selected");
    });
    // A reload answers column by column; the first reply must not prune a
    // selection made in a column whose reply is still on its way.
    it("keeps a selected ticket that is moving between columns until both replies are in", async () => {
      const user = userEvent.setup();
      let all = [makeTicket({ id: 1, title: "Mover", status: "preparing" })];
      serve(() => all);
      await renderSignedIn(<FilteredBoardView />);
      await user.click(await screen.findByRole("button", { name: "Select" }));
      await user.click(await screen.findByText("Mover"));
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("1 selected");

      // It moves to Open; Preparing's reply (without it) lands first.
      all = [makeTicket({ id: 1, title: "Mover", status: "open" })];
      let resolveOpen!: (page: Page) => void;
      vi.mocked(apiClient.listTicketsPage).mockImplementation(
        (filter: Filter) =>
          filter.statuses?.[0] === "open"
            ? new Promise((resolve) => (resolveOpen = resolve))
            : Promise.resolve({
                tickets: all.filter((t) => filter.statuses?.includes(t.status)),
              }),
      );
      boardChanged();
      await waitFor(() =>
        expect(screen.queryByText("Mover")).not.toBeInTheDocument(),
      );

      await act(async () => resolveOpen({ tickets: all }));
      const moved = await within(column("Open")).findByText("Mover");
      expect(moved.closest(".ticket-card__select")).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("1 selected");
    });
  });

  describe("select mode", () => {
    const PREPARING: Partial<Ticket> = {
      status: "preparing",
      allowed_moves: ["preparing", "parked", "done", "cancelled"],
    };

    function card(title: string): HTMLElement {
      return screen
        .getByText(title)
        .closest(".ticket-card__select") as HTMLElement;
    }

    async function enterSelectMode(user: ReturnType<typeof userEvent.setup>) {
      await user.click(await screen.findByRole("button", { name: "Select" }));
    }

    it("makes each card a toggle rather than a link, until Esc", async () => {
      const user = userEvent.setup();
      serve(() => [makeTicket({ id: 1, title: "First", status: "open" })]);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("First");
      expect(card("First").tagName).toBe("A");

      await enterSelectMode(user);
      expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(card("First")).toHaveAttribute("role", "button");
      expect(card("First")).toHaveAttribute("aria-pressed", "false");

      await user.click(card("First"));
      expect(card("First")).toHaveAttribute("aria-pressed", "true");
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("1 selected");

      await user.keyboard("{Escape}");
      expect(card("First").tagName).toBe("A");
      expect(
        screen.queryByRole("region", { name: "Bulk actions" }),
      ).not.toBeInTheDocument();
    });

    it("toggles a card from the keyboard", async () => {
      const user = userEvent.setup();
      serve(() => [makeTicket({ id: 1, title: "First", status: "open" })]);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("First");
      await enterSelectMode(user);

      card("First").focus();
      await user.keyboard(" ");
      expect(card("First")).toHaveAttribute("aria-pressed", "true");
      await user.keyboard("{Enter}");
      expect(card("First")).toHaveAttribute("aria-pressed", "false");
    });

    it("selects and clears a whole column", async () => {
      const user = userEvent.setup();
      serve(() => [
        makeTicket({ id: 1, title: "Idea one", status: "preparing" }),
        makeTicket({ id: 2, title: "Idea two", status: "preparing" }),
        makeTicket({ id: 3, title: "Live one", status: "open" }),
      ]);
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Live one");
      await enterSelectMode(user);
      await user.click(card("Live one"));

      expect(
        screen.getByRole("button", { name: "Clear Preparing" }),
      ).toBeDisabled();

      await user.click(
        screen.getByRole("button", { name: "Select all in Preparing" }),
      );
      expect(card("Idea one")).toHaveAttribute("aria-pressed", "true");
      expect(card("Idea two")).toHaveAttribute("aria-pressed", "true");
      expect(
        screen.getByRole("region", { name: "Bulk actions" }),
      ).toHaveTextContent("3 selected");
      expect(
        screen.getByRole("button", { name: "Select all in Preparing" }),
      ).toBeDisabled();

      await user.click(screen.getByRole("button", { name: "Clear Preparing" }));
      expect(card("Idea one")).toHaveAttribute("aria-pressed", "false");
      expect(card("Live one")).toHaveAttribute("aria-pressed", "true");
      expect(
        screen.getByRole("button", { name: "Clear Preparing" }),
      ).toBeDisabled();
      expect(
        screen.getByRole("button", { name: "Select all in Preparing" }),
      ).toBeEnabled();
    });

    it("applies an action to the selection, then leaves select mode and reloads", async () => {
      const user = userEvent.setup();
      let all = [
        makeTicket({ id: 1, title: "Idea one", ...PREPARING }),
        makeTicket({ id: 2, title: "Idea two", ...PREPARING }),
      ];
      serve(() => all);
      vi.spyOn(apiClient, "bulkSetTicketStatus").mockImplementation(
        async () => {
          all = [];
          return [];
        },
      );
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Idea one");
      await enterSelectMode(user);
      await user.click(
        screen.getByRole("button", { name: "Select all in Preparing" }),
      );
      const before = requests().length;

      await chooseFromDropdown(user, "Move to", "Parked");

      expect(apiClient.bulkSetTicketStatus).toHaveBeenCalledWith(
        [1, 2],
        "parked",
      );
      await waitFor(() =>
        expect(screen.queryByText("Idea one")).not.toBeInTheDocument(),
      );
      expect(requests().length).toBe(before + LIVE.length);
      expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute(
        "aria-pressed",
        "false",
      );
    });

    // The bar calls back once its request is done; a filter ticked while it
    // was out must be what the board reloads with.
    it("reloads with the filter as it is when the action finishes", async () => {
      const user = userEvent.setup();
      serve(() => [makeTicket({ id: 1, title: "Idea one", ...PREPARING })]);
      let finish!: () => void;
      vi.spyOn(apiClient, "bulkSetTicketStatus").mockReturnValue(
        new Promise((resolve) => (finish = () => resolve([]))),
      );
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Idea one");
      await enterSelectMode(user);
      await user.click(card("Idea one"));
      await chooseFromDropdown(user, "Move to", "Parked");

      await user.click(screen.getByRole("checkbox", { name: "Include done" }));
      await within(column("Done")).findByText("No tickets");
      const before = requests().length;
      await act(async () => finish());

      expect(
        requestedStatuses()
          .slice(before)
          .map((s) => s[0])
          .sort(),
      ).toEqual([...LIVE, "done"].sort());
    });

    // Nothing moved, so the selection stays for another go -- less any
    // ticket the reloaded board no longer has.
    it("reloads the board after a refusal and keeps the selection, less what left the board", async () => {
      const user = userEvent.setup();
      let all = [
        makeTicket({ id: 1, title: "Idea one", ...PREPARING }),
        makeTicket({ id: 2, title: "Idea two", ...PREPARING }),
        makeTicket({ id: 3, title: "Idea three", ...PREPARING }),
      ];
      serve(() => all);
      vi.spyOn(apiClient, "bulkSetTicketStatus").mockImplementation(
        async () => {
          all = [
            makeTicket({ id: 1, title: "Idea one", ...PREPARING }),
            makeTicket({
              id: 2,
              title: "Idea two",
              status: "open",
              allowed_moves: ["open", "parked", "done"],
            }),
          ];
          throw new BulkRefusedError(
            404,
            "2 ticket(s) refused; nothing was changed.",
            [
              {
                id: 2,
                status: "open",
                message: "#2 open: whatever the rule says",
              },
              { id: 3, status: null, message: "#3: ticket not found" },
            ],
          );
        },
      );
      await renderSignedIn(<FilteredBoardView />);
      await screen.findByText("Idea one");
      await enterSelectMode(user);
      await user.click(
        screen.getByRole("button", { name: "Select all in Preparing" }),
      );

      await chooseFromDropdown(user, "Move to", "Parked");

      await within(column("Open")).findByText("Idea two");
      expect(screen.queryByText("Idea three")).not.toBeInTheDocument();
      const bar = screen.getByRole("region", { name: "Bulk actions" });
      expect(bar).toHaveTextContent("2 selected");
      expect(within(bar).getByRole("alert")).toHaveTextContent(
        "#2 open: whatever the rule says",
      );

      await user.click(
        within(bar).getByRole("button", {
          name: "Deselect the 1 that can't make this change",
        }),
      );

      expect(bar).toHaveTextContent("1 selected");
      expect(card("Idea one")).toHaveAttribute("aria-pressed", "true");
      expect(card("Idea two")).toHaveAttribute("aria-pressed", "false");
    });
  });
});

// A ticket with two attempts is two cards: attempt 2 still in progress,
// attempt 1 finished in Review awaiting a choice.
describe("BoardView attempt cards", () => {
  const stamp = "2026-01-01T00:00:00Z";
  const inProgress = () =>
    makeAttemptCard(
      { id: 42, title: "Two ways", status: "in_progress", updated_at: stamp },
      { number: 2 },
    );
  const inReview = () =>
    makeAttemptCard(
      { id: 42, title: "Two ways", status: "review", updated_at: stamp },
      { number: 1, awaiting_choice: true },
    );
  let changed: (revision: number, change: Partial<BoardChange>) => void;

  beforeEach(() => {
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue({ tickets: [] });
    vi.spyOn(apiClient, "listBoardColumn").mockImplementation(
      async (filter) => ({
        tickets:
          filter.statuses?.[0] === "in_progress"
            ? [inProgress()]
            : filter.statuses?.[0] === "review"
              ? [inReview()]
              : [],
      }),
    );
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange) => {
      changed = (revision, change) =>
        onChange(revision, { revision, ...change });
      return vi.fn();
    });
  });

  it("draws one card of the ticket in In progress and one in Review", async () => {
    await renderSignedIn(<FilteredBoardView />);

    const working = await within(column("In progress")).findByText("Two ways");
    expect(working.closest(".ticket-card")).toHaveTextContent("#42 · 2");
    const review = within(column("Review"))
      .getByText("Two ways")
      .closest(".ticket-card");
    expect(review).toHaveTextContent("#42 · 1");
    expect(review).toHaveTextContent("awaiting choice");
    // Only the work columns' first pages are the board's; the others stay the tickets route.
    expect(
      vi
        .mocked(apiClient.listBoardColumn)
        .mock.calls.map(([f]) => f.statuses)
        .sort(),
    ).toEqual([["discussion"], ["in_progress"], ["review"]]);
    expect(requestedStatuses().some((s) => s[0] === "in_progress")).toBe(false);
  });

  it("continues a work column with the tickets route and the cursor the board gave", async () => {
    const user = userEvent.setup();
    vi.mocked(apiClient.listBoardColumn).mockImplementation(async (filter) =>
      filter.statuses?.[0] === "in_progress"
        ? { tickets: [inProgress()], nextCursor: "cards:1" }
        : { tickets: [] },
    );
    const second = makeAttemptCard(
      { id: 42, title: "Two ways", status: "in_progress" },
      { number: 1 },
    );
    vi.mocked(apiClient.listTicketsPage).mockImplementation(
      async (filter: Filter) =>
        filter.cursor === "cards:1" ? { tickets: [second] } : { tickets: [] },
    );
    await renderSignedIn(<FilteredBoardView />);

    await user.click(
      await within(column("In progress")).findByRole("button", {
        name: "Load more",
      }),
    );

    await waitFor(() =>
      expect(
        within(column("In progress")).getAllByText("Two ways"),
      ).toHaveLength(2),
    );
    expect(
      vi
        .mocked(apiClient.listTicketsPage)
        .mock.calls.filter(
          ([f]) => f.cursor === "cards:1" && f.statuses?.[0] === "in_progress",
        ),
    ).toHaveLength(1);
    expect(
      vi
        .mocked(apiClient.listBoardColumn)
        .mock.calls.every(([f]) => f.cursor === undefined),
    ).toBe(true);
  });

  it("redraws both cards when the stream names the ticket", async () => {
    await renderSignedIn(<FilteredBoardView />);
    await within(column("Review")).findByText("Two ways");
    const before = vi.mocked(apiClient.listBoardColumn).mock.calls.length;

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    act(() => {
      changed(2, {
        tickets: [
          makeTicket({
            id: 42,
            title: "Renamed",
            status: "in_progress",
            updated_at: "2099-01-01T00:00:00Z",
          }),
        ],
        cards: [
          {
            ...inProgress(),
            title: "Renamed",
            updated_at: "2099-01-01T00:00:00Z",
          },
          {
            ...inReview(),
            title: "Renamed",
            updated_at: "2099-01-01T00:00:00Z",
          },
        ],
      });
      vi.advanceTimersByTime(2 * LIVE_REFRESH_THROTTLE_MS);
    });
    vi.useRealTimers();

    expect(
      await within(column("In progress")).findByText("Renamed"),
    ).toBeInTheDocument();
    expect(within(column("Review")).getByText("Renamed")).toBeInTheDocument();
    expect(
      within(column("In progress")).getAllByText(/Renamed|Two ways/),
    ).toHaveLength(1);
    expect(vi.mocked(apiClient.listBoardColumn).mock.calls).toHaveLength(
      before,
    );
  });

  it("selects the ticket from either card and counts it once", async () => {
    const user = userEvent.setup();
    await renderSignedIn(<FilteredBoardView />);
    await user.click(await screen.findByRole("button", { name: "Select" }));

    await user.click(
      await within(column("In progress")).findByText("Two ways"),
    );
    expect(
      screen.getByRole("region", { name: "Bulk actions" }),
    ).toHaveTextContent("1 selected");
    // The other card of the ticket is marked too.
    expect(
      within(column("Review"))
        .getByText("Two ways")
        .closest(".ticket-card__select"),
    ).toHaveAttribute("aria-pressed", "true");

    await user.click(within(column("Review")).getByText("Two ways"));
    expect(
      screen.queryByRole("region", { name: "Bulk actions" }),
    ).toHaveTextContent("0 selected");
  });

  it("counts a ticket once when both of its cards are selected through select all", async () => {
    const user = userEvent.setup();
    vi.mocked(apiClient.listBoardColumn).mockImplementation(async (filter) => ({
      tickets:
        filter.statuses?.[0] === "review"
          ? [
              inReview(),
              makeAttemptCard(
                { id: 42, title: "Two ways", status: "review" },
                { number: 2 },
              ),
            ]
          : [],
    }));
    await renderSignedIn(<FilteredBoardView />);
    await user.click(await screen.findByRole("button", { name: "Select" }));

    await user.click(
      await within(column("Review")).findByRole("button", {
        name: "Select all in Review",
      }),
    );

    expect(
      screen.getByRole("region", { name: "Bulk actions" }),
    ).toHaveTextContent("1 selected");
  });
});

function headingOf(status: Status): string {
  return {
    parked: "Parked",
    preparing: "Preparing",
    open: "Open",
    in_progress: "In progress",
    in_merge_queue: "In merge queue",
    blocked: "Blocked",
    discussion: "Discussion",
    review: "Review",
    done: "Done",
    cancelled: "Cancelled",
  }[status];
}
