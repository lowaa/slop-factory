import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeBlockingGraph, makeGraphEdge, makeGraphNode, makeProfile, makeTicket } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { LoggerContext } from "@/context/logger";
import BlockingGraphView from "./blocking_graph";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import { BLOCKED_FILTER_MODEL, DEFAULT_BLOCKED_VALUES } from "./blocked_filter";
import { chooseFromDropdown } from "@/test/widgets";

const logger = { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn(), event: vi.fn() };

function view(values: FilterValues = DEFAULT_BLOCKED_VALUES, onFilterChange: (v: FilterValues) => void = () => {}) {
  return (
    <LoggerContext.Provider value={logger}>
      <BlockingGraphView values={values} onFilterChange={onFilterChange} />
    </LoggerContext.Provider>
  );
}

const tickets = [
  makeTicket({ id: 18, title: "Build the API", status: "blocked", unblocks: 2 }),
  makeTicket({ id: 19, title: "Build the UI", status: "blocked" }),
];

function mockPage(page = { tickets }) {
  return vi.spyOn(apiClient, "listBlockedPage").mockResolvedValue(page);
}

const chain = makeBlockingGraph({
  nodes: [
    makeGraphNode({ id: 17, title: "Design the schema", layer: 0 }),
    makeGraphNode({ id: 18, title: "Build the API", blocked_by: [17], blocked: true, layer: 1 }),
  ],
  edges: [makeGraphEdge({ blocker: 17, blocked: 18 })],
  layers: [[17], [18]],
});

describe("BlockingGraphView", () => {
  it("lists the blocked tickets with what each holds up, and draws no graph until one is opened", async () => {
    mockPage();
    const getBlockers = vi.spyOn(apiClient, "getTicketBlockers");

    await renderSignedIn(view());

    expect(await screen.findByText("Build the API")).toBeInTheDocument();
    expect(screen.getByText("Build the UI")).toBeInTheDocument();
    expect(screen.getByText("unblocks 2")).toBeInTheDocument();
    expect(screen.getAllByText(/^unblocks/)).toHaveLength(1);
    expect(document.querySelector(".blocking-graph__layers")).not.toBeInTheDocument();
    expect(getBlockers).not.toHaveBeenCalled();
  });

  it("opens the clicked row's own graph as ordered layers, and hides it again", async () => {
    const user = userEvent.setup();
    mockPage();
    const getBlockers = vi.spyOn(apiClient, "getTicketBlockers").mockResolvedValue(chain);

    await renderSignedIn(view());
    await user.click(await screen.findByRole("button", { name: "Show graph for #18" }));

    // The id of the row opened, not the first on screen.
    await waitFor(() => expect(getBlockers).toHaveBeenCalledWith(18));
    expect(await screen.findAllByText(/^Layer \d$/)).toHaveLength(2);
    const columns = document.querySelectorAll(".blocking-graph__layer");
    expect(within(columns[0] as HTMLElement).getByText("Design the schema")).toBeInTheDocument();
    expect(within(columns[1] as HTMLElement).getByText("Build the API")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Hide graph for #18" })).toHaveAttribute("aria-expanded", "true");

    await user.click(screen.getByRole("button", { name: "Hide graph for #18" }));
    expect(document.querySelector(".blocking-graph__layers")).not.toBeInTheDocument();
  });

  it("marks a hidden blocker in an opened graph", async () => {
    const user = userEvent.setup();
    mockPage();
    vi.spyOn(apiClient, "getTicketBlockers").mockResolvedValue(
      makeBlockingGraph({
        nodes: [
          makeGraphNode({ id: 1, title: "Parked blocker", status: "parked", hidden: true, layer: 0 }),
          makeGraphNode({ id: 18, title: "Waiting ticket", blocked: true, blocked_by: [1], layer: 1 }),
        ],
        edges: [makeGraphEdge({ blocker: 1, blocked: 18 })],
        layers: [[1], [18]],
      }),
    );

    await renderSignedIn(view());
    await user.click(await screen.findByRole("button", { name: "Show graph for #18" }));

    const marker = (await screen.findByText("Parked blocker")).closest("a") as HTMLElement;
    expect(marker).toHaveClass("blocking-graph__card--hidden");
    expect(within(marker).getByText("hidden by filter, still blocking")).toBeInTheDocument();
    expect(within(screen.getByText("Waiting ticket").closest("a") as HTMLElement).getByText("blocked by #1")).toBeInTheDocument();
  });

  it("says when a graph fails to load", async () => {
    const user = userEvent.setup();
    mockPage();
    vi.spyOn(apiClient, "getTicketBlockers").mockRejectedValue(new ApiError(500, "boom"));

    await renderSignedIn(view());
    await user.click(await screen.findByRole("button", { name: "Show graph for #18" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
  });

  it("opens a row's ticket, naming the blocked tab and the session's tenant", async () => {
    const user = userEvent.setup();
    mockPage();

    const { router } = await renderSignedIn(view(), {
      profile: makeProfile({ tenant_id: "tenant-a" }),
    });
    await user.click(await screen.findByText("Build the UI"));

    expect(router.state.location.href).toBe("/tickets/19?from=blocked&tenant=tenant-a");
  });

  it("loads the next page on request", async () => {
    const user = userEvent.setup();
    const spy = vi
      .spyOn(apiClient, "listBlockedPage")
      .mockResolvedValueOnce({ tickets: [tickets[0]], nextCursor: "c2" })
      .mockResolvedValueOnce({ tickets: [tickets[1]] });

    await renderSignedIn(view());
    await user.click(await screen.findByRole("button", { name: "Load more" }));

    expect(await screen.findByText("Build the UI")).toBeInTheDocument();
    expect(spy.mock.calls[1][0].cursor).toBe("c2");
  });

  // The page's default: tickets nobody is working on stay out of the list.
  it("asks for every status but parked, cancelled and done by default", async () => {
    const spy = mockPage();

    await renderSignedIn(view());

    await waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    const status = spy.mock.calls[0][0].status as string[];
    expect(status).toEqual(expect.arrayContaining(["preparing", "open", "in_progress", "blocked", "discussion", "review"]));
    for (const hidden of ["parked", "cancelled", "done"]) expect(status).not.toContain(hidden);
  });

  it("asks for a hidden status and the other filters once set", async () => {
    const spy = mockPage();
    const values = BLOCKED_FILTER_MODEL.fromSearch({ exclude: "cancelled,done", q: "login", priority: "high", updated_from: "2026-10-01" });

    await renderSignedIn(view(values, () => {}));

    await waitFor(() => expect(spy).toHaveBeenCalled());
    expect(spy.mock.calls[0][0]).toMatchObject({ q: "login", priority: ["high"], updated_from: "2026-10-01" });
    expect(spy.mock.calls[0][0].status).toContain("parked");
  });

  it("shows a plain empty state when nothing is blocked, and a filtered one when a filter hides it", async () => {
    mockPage({ tickets: [] });
    const { unmount } = await renderSignedIn(view());
    expect(await screen.findByText("Nothing is blocked.")).toBeInTheDocument();
    unmount();

    await renderSignedIn(view(BLOCKED_FILTER_MODEL.fromSearch({ q: "zebra" }), () => {}));
    expect(await screen.findByText("No tickets match these filters.")).toBeInTheDocument();
  });

  it("makes no request when every status is hidden", async () => {
    const spy = mockPage();
    const everything = { ...DEFAULT_BLOCKED_VALUES, status: [...FILTER_STATUSES] };

    await renderSignedIn(view(everything, () => {}));

    expect(await screen.findByText(/no tickets match/)).toBeInTheDocument();
    expect(spy).not.toHaveBeenCalled();
  });

  it("hands the filter on when it is changed", async () => {
    const user = userEvent.setup();
    mockPage();
    const onChange = vi.fn();

    await renderSignedIn(view(DEFAULT_BLOCKED_VALUES, onChange));
    await chooseFromDropdown(user, "Priority", "urgent");

    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ priority: "urgent" }));
  });

  it("shows a failed page load and retries it", async () => {
    const user = userEvent.setup();
    const spy = vi
      .spyOn(apiClient, "listBlockedPage")
      .mockRejectedValueOnce(new ApiError(500, "boom"))
      .mockResolvedValueOnce({ tickets });

    await renderSignedIn(view());
    expect(await screen.findByRole("alert")).toHaveTextContent("boom");

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Build the API")).toBeInTheDocument();
    expect(spy).toHaveBeenCalledTimes(2);
  });

  it("starts the list again from the top when the filter changes", async () => {
    const user = userEvent.setup();
    const spy = vi
      .spyOn(apiClient, "listBlockedPage")
      .mockResolvedValueOnce({ tickets: [tickets[0]], nextCursor: "c2" })
      .mockResolvedValueOnce({ tickets: [tickets[1]] });

    function Harness() {
      const [values, setValues] = useState<FilterValues>(DEFAULT_BLOCKED_VALUES);
      return view(values, setValues);
    }
    await renderSignedIn(<Harness />);
    await screen.findByText("Build the API");
    await chooseFromDropdown(user, "Priority", "urgent");

    expect(await screen.findByText("Build the UI")).toBeInTheDocument();
    expect(screen.queryByText("Build the API")).not.toBeInTheDocument();
    expect(spy.mock.calls[1][0]).toMatchObject({ priority: ["urgent"], cursor: undefined });
  });
});
