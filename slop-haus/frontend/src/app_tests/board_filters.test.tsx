import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile, makeTicket } from "@/test/fixtures";
import { TEXT_DEBOUNCE_MS } from "@/components/ticket_filter";
import { forgetRememberedFilters } from "@/components/ticket_filter";
import { boardRequests, lastRequestedStatuses, serveTickets } from "@/test/serve_tickets";
import { chooseFromDropdown } from "@/test/widgets";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { clickAccountLink } from "@/test/header";

// The board's filters live in the address, so a filtered board is a link
// that can be reloaded or shared.
describe("the board's filters", () => {
  function signIn() {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: "tenant-a" }));
    mockBoardShell();
  }

  function search(): Record<string, string> {
    return Object.fromEntries(new URLSearchParams(window.location.search));
  }

  function mockOpenableTicket() {
    serveTickets(() => [makeTicket({ id: 7, title: "Zebra import" })]);
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Zebra import" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
    vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
    vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  }

  // The remembered filter is module state, which outlives a test.
  afterEach(() => {
    forgetRememberedFilters();
  });

  it("opens with the filters its address names", async () => {
    window.history.pushState({}, "", "/?done=true&q=zebra%20import");
    signIn();

    renderApp();

    await waitFor(() => expect(screen.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("zebra import"));
    expect(screen.getByRole("checkbox", { name: "Include done" })).toBeChecked();
    await waitFor(() => expect(boardRequests().map((r) => r.statuses![0])).toContain("done"));
    expect(boardRequests().every((r) => r.text === "zebra import")).toBe(true);
  });

  // A board showing parked tickets survives a reload, and can be shared.
  it("opens with parked tickets included when its address says so, and puts the choice there", async () => {
    window.history.pushState({}, "", "/?parked=true");
    signIn();
    const user = userEvent.setup();

    renderApp();

    const includeParked = await screen.findByRole("checkbox", { name: "Include parked" });
    await waitFor(() => expect(includeParked).toBeChecked());
    await waitFor(() => expect(boardRequests().map((r) => r.statuses![0])).toContain("parked"));

    await user.click(includeParked);
    await waitFor(() => expect(search()).toEqual({}));
    await user.click(includeParked);
    await waitFor(() => expect(search()).toEqual({ parked: "true" }));
  });

  // A flag is present or absent: anything but true in the address is
  // absent, not a truthy string.
  it("leaves parked tickets out for any other value of ?parked=", async () => {
    window.history.pushState({}, "", "/?parked=false");
    signIn();

    renderApp();

    const includeParked = await screen.findByRole("checkbox", { name: "Include parked" });
    await waitFor(() => expect(boardRequests().length).toBeGreaterThan(0));
    expect(includeParked).not.toBeChecked();
    expect(boardRequests().map((r) => r.statuses![0])).not.toContain("parked");
  });

  it("puts each filter in the address, in place of the entry it was on, keeping the others", async () => {
    window.history.pushState({}, "", "/?q=zebra&column=review");
    signIn();
    const user = userEvent.setup();

    renderApp();
    const includeDone = await screen.findByRole("checkbox", { name: "Include done" });
    const entries = window.history.length;
    await user.click(includeDone);

    await waitFor(() => expect(search()).toEqual({ q: "zebra", column: "review", done: "true" }));

    // fireEvent and fake timers for the typing: see ticket_filter.test.tsx.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    onTestFinished(() => void vi.useRealTimers());
    fireEvent.change(screen.getByRole("searchbox", { name: "Search tickets" }), { target: { value: "" } });
    act(() => vi.advanceTimersByTime(TEXT_DEBOUNCE_MS));
    vi.useRealTimers();

    await waitFor(() => expect(search()).toEqual({ column: "review", done: "true" }));
    await user.click(includeDone);
    await waitFor(() => expect(search()).toEqual({ column: "review" }));
    expect(window.history.length).toBe(entries);
  });

  // The sort has its own key; the filter reads and writes only its own.
  it("keeps the sort in its own address key, left out at the default, apart from the filter", async () => {
    window.history.pushState({}, "", "/?q=zebra&sort=priority");
    signIn();
    const user = userEvent.setup();

    renderApp();
    const sortBox = await screen.findByRole("combobox", { name: "Sort by" });
    expect(sortBox).toHaveTextContent("Priority (urgent first)");
    await waitFor(() => expect(boardRequests().at(-1)).toMatchObject({ text: "zebra", sort: "priority" }));

    await chooseFromDropdown(user, "Sort by", "Oldest first");
    await waitFor(() => expect(search()).toEqual({ q: "zebra", sort: "oldest" }));
    await waitFor(() => expect(boardRequests().at(-1)).toMatchObject({ text: "zebra", sort: "oldest", cursor: undefined }));

    // Not filtering, so nothing to clear until something is; clearing the filter keeps the sort.
    await user.click(screen.getByRole("checkbox", { name: "Include parked" }));
    await user.click(await screen.findByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(search()).toEqual({ sort: "oldest" }));
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();

    await chooseFromDropdown(user, "Sort by", "Newest first");
    await waitFor(() => expect(search()).toEqual({}));
    await waitFor(() => expect(boardRequests().at(-1)).toMatchObject({ sort: "newest" }));
  });

  it("ignores a sort the board does not offer", async () => {
    window.history.pushState({}, "", "/?sort=recently_closed");
    signIn();

    renderApp();

    expect(await screen.findByRole("combobox", { name: "Sort by" })).toHaveTextContent("Newest first");
    await waitFor(() => expect(boardRequests().at(-1)).toMatchObject({ sort: "newest" }));
  });

  it("is kept, with the sort, by a ticket's Back link", async () => {
    window.history.pushState({}, "", "/?q=zebra&sort=priority");
    signIn();
    mockOpenableTicket();
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByText("Zebra import"));
    await user.click(await screen.findByRole("link", { name: "← Back to board" }));

    await waitFor(() => expect(window.location.pathname).toBe("/"));
    expect(search()).toEqual({ q: "zebra", sort: "priority" });
    expect(screen.getByRole("combobox", { name: "Sort by" })).toHaveTextContent("Priority (urgent first)");
  });

  it("clears when the header's Board tab is followed", async () => {
    window.history.pushState({}, "", "/?done=true&q=zebra");
    signIn();
    const user = userEvent.setup();

    renderApp();
    const box = await screen.findByRole("searchbox", { name: "Search tickets" });
    await waitFor(() => expect(box).toHaveValue("zebra"));
    await user.click(screen.getByRole("link", { name: "Board" }));

    await waitFor(() => expect(box).toHaveValue(""));
    expect(screen.getByRole("checkbox", { name: "Include done" })).not.toBeChecked();
    await waitFor(() => expect(lastRequestedStatuses(6)).not.toContain("done"));
    expect(boardRequests().at(-1)?.text).toBeUndefined();
  });

  it("is kept by a ticket's Back link", async () => {
    window.history.pushState({}, "", "/?done=true&q=zebra");
    signIn();
    mockOpenableTicket();
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByText("Zebra import"));
    await user.click(await screen.findByRole("link", { name: "← Back to board" }));

    await waitFor(() => expect(window.location.pathname).toBe("/"));
    expect(search()).toEqual({ done: "true", q: "zebra" });
    expect(screen.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("zebra");
    expect(screen.getByRole("checkbox", { name: "Include done" })).toBeChecked();
    await waitFor(() => expect(boardRequests().at(-1)).toMatchObject({ text: "zebra" }));
    expect(lastRequestedStatuses(7)).toContain("done");
  });

  // Remembered as last seen, so one cleared since is not brought back.
  it("is not brought back by a ticket's Back link once cleared", async () => {
    window.history.pushState({}, "", "/?done=true&q=zebra");
    signIn();
    mockOpenableTicket();
    const user = userEvent.setup();

    renderApp();
    const box = await screen.findByRole("searchbox", { name: "Search tickets" });
    await waitFor(() => expect(box).toHaveValue("zebra"));
    await user.click(screen.getByRole("link", { name: "Board" }));
    await waitFor(() => expect(box).toHaveValue(""));
    await user.click(await screen.findByText("Zebra import"));

    expect(await screen.findByRole("link", { name: "← Back to board" })).toHaveAttribute("href", "/");
  });

  // On a shared browser the next person starts from an unfiltered board.
  it("is forgotten on sign-out", async () => {
    window.history.pushState({}, "", "/?q=zebra");
    signIn();
    mockOpenableTicket();
    vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
    const user = userEvent.setup();

    const { router } = renderApp();
    await waitFor(() => expect(screen.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("zebra"));
    await clickAccountLink(user, "Sign out");
    await screen.findByRole("heading", { name: "Sign in" });

    // A new page load, straight to a ticket: the signed-out router is
    // torn down first, or it would send the new address to /login.
    cleanup();
    router.history.destroy();
    window.history.pushState({}, "", "/tickets/7");
    renderApp();
    expect(await screen.findByRole("link", { name: "← Back to board" })).toHaveAttribute("href", "/");
  });
});
