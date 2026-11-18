import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeAgentContext } from "@/test/fixtures";
import AgentContextHistory from "./agent_context_history";

const OLDER = makeAgentContext({
  id: 1,
  created_at: "2026-03-01T09:00:00Z",
  body: "Branch ticket-9-x, tests pass.\nNext: wire up the route.",
});
const NEWER = makeAgentContext({
  id: 2,
  created_at: "2026-03-02T15:30:00Z",
  body: "Route wired.\nWaiting on the human for the URL shape.",
});

// Each entry's <details>, newest first as the list shows them.
function entries(): HTMLDetailsElement[] {
  return within(screen.getByRole("list"))
    .getAllByRole("listitem")
    .map((li) => li.querySelector("details")!);
}

describe("AgentContextHistory", () => {
  it("lists every handoff newest first, each with its timestamp", () => {
    render(<AgentContextHistory contexts={[OLDER, NEWER]} />);

    const [first, second] = entries();
    expect(first.querySelector("time")).toHaveAttribute("datetime", NEWER.created_at);
    expect(second.querySelector("time")).toHaveAttribute("datetime", OLDER.created_at);
    expect(screen.getByText("2 handoffs, newest first.")).toBeInTheDocument();
  });

  it("opens only the latest, and shows the others by their first line", () => {
    render(<AgentContextHistory contexts={[OLDER, NEWER]} />);

    const [latest, older] = entries();
    expect(latest.open).toBe(true);
    expect(older.open).toBe(false);
    expect(within(older).getByText("Branch ticket-9-x, tests pass.")).toBeVisible();
    expect(within(older).getByText(/Next: wire up the route\./)).not.toBeVisible();
  });

  it("expands and collapses one entry at a time", async () => {
    const user = userEvent.setup();
    render(<AgentContextHistory contexts={[OLDER, NEWER]} />);

    const [latest, older] = entries();
    await user.click(within(older).getByText("Branch ticket-9-x, tests pass."));
    expect(older.open).toBe(true);
    expect(within(older).getByText(/Next: wire up the route\./)).toBeVisible();
    expect(latest.open).toBe(true);

    await user.click(latest.querySelector("summary")!);
    expect(latest.open).toBe(false);
    expect(older.open).toBe(true);
  });

  it("expands and collapses them all at once", async () => {
    const user = userEvent.setup();
    render(<AgentContextHistory contexts={[OLDER, NEWER]} />);

    await user.click(screen.getByRole("button", { name: "Expand all" }));
    expect(entries().map((d) => d.open)).toEqual([true, true]);

    await user.click(screen.getByRole("button", { name: "Collapse all" }));
    expect(entries().map((d) => d.open)).toEqual([false, false]);
    expect(screen.getByRole("button", { name: "Expand all" })).toBeInTheDocument();
  });

  // After the human answers a discussion, the page refetches the handoffs;
  // one written meanwhile is the new latest, and opens as the first did.
  it("opens a handoff that arrives while it is on screen, leaving the others as they were", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<AgentContextHistory contexts={[OLDER, NEWER]} />);
    // The reverse of how they start: the newer folded, the older open.
    const [newer, older] = entries();
    await user.click(newer.querySelector("summary")!);
    await user.click(older.querySelector("summary")!);
    expect(entries().map((d) => d.open)).toEqual([false, true]);

    const NEWEST = makeAgentContext({ id: 3, created_at: "2026-03-03T08:00:00Z", body: "Picked it back up." });
    rerender(<AgentContextHistory contexts={[OLDER, NEWER, NEWEST]} />);

    expect(entries().map((d) => d.open)).toEqual([true, false, true]);
  });

  it("says so when there are none", () => {
    render(<AgentContextHistory contexts={[]} />);

    expect(screen.getByText("No agent has left a handoff on this ticket yet.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Expand all" })).not.toBeInTheDocument();
  });
});
