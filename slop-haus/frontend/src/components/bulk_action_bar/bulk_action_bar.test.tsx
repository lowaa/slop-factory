import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, BulkRefusedError, type Ticket } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { chooseFromDropdown } from "@/test/widgets";
import BulkActionBar from "./bulk_action_bar";

function renderBar(selected: Ticket[]) {
  const props = {
    selected,
    onApplied: vi.fn(),
    onRefused: vi.fn(),
    onDeselect: vi.fn(),
    onExit: vi.fn(),
  };
  const view = render(<BulkActionBar {...props} />);
  return { ...props, rerender: (next: Ticket[]) => view.rerender(<BulkActionBar {...props} selected={next} />) };
}

async function moveOptions(user: ReturnType<typeof userEvent.setup>): Promise<string[]> {
  await user.click(screen.getByRole("combobox", { name: "Move to" }));
  return screen.getAllByRole("option").map((o) => o.textContent ?? "");
}

const PREPARING: Partial<Ticket> = {
  status: "preparing",
  allowed_moves: ["preparing", "parked", "done", "cancelled"],
  can_ready: true,
};

describe("BulkActionBar", () => {
  it("shows how many tickets are selected", () => {
    renderBar([makeTicket({ id: 1 }), makeTicket({ id: 2 })]);

    expect(screen.getByRole("status")).toHaveTextContent("2 selected");
  });

  it("offers only the moves every selected ticket allows", async () => {
    const user = userEvent.setup();
    renderBar([
      makeTicket({ id: 1, status: "open", allowed_moves: ["preparing", "open", "in_progress", "done"] }),
      makeTicket({ id: 2, status: "review", allowed_moves: ["preparing", "discussion", "review", "done"] }),
    ]);

    expect(await moveOptions(user)).toEqual(["Preparing", "Done"]);
  });

  it("offers Open when every selected ticket can be readied", async () => {
    const user = userEvent.setup();
    renderBar([makeTicket({ id: 1, ...PREPARING }), makeTicket({ id: 2, ...PREPARING })]);

    expect(await moveOptions(user)).toEqual(["Open", "Parked", "Done", "Cancelled"]);
  });

  it("leaves Open out when any selected ticket can't be readied", async () => {
    const user = userEvent.setup();
    renderBar([makeTicket({ id: 1, ...PREPARING }), makeTicket({ id: 2, ...PREPARING, can_ready: false })]);

    expect(await moveOptions(user)).not.toContain("Open");
  });

  it("says why, rather than showing an empty menu, when there's no move in common", () => {
    renderBar([
      makeTicket({ id: 1, status: "preparing", allowed_moves: ["preparing", "parked"] }),
      makeTicket({ id: 2, status: "in_progress", allowed_moves: ["open", "in_progress"] }),
    ]);

    expect(screen.queryByRole("combobox", { name: "Move to" })).not.toBeInTheDocument();
    expect(screen.getByText(/Preparing and In progress tickets allow different moves/)).toBeInTheDocument();
    // Priority still applies to anything.
    expect(screen.getByRole("combobox", { name: "Priority" })).toBeInTheDocument();
  });

  it("moves every selected ticket in one call, and reports it applied", async () => {
    const user = userEvent.setup();
    const bulk = vi.spyOn(apiClient, "bulkSetTicketStatus").mockResolvedValue([]);
    const bar = renderBar([makeTicket({ id: 4, ...PREPARING }), makeTicket({ id: 9, ...PREPARING })]);

    await chooseFromDropdown(user, "Move to", "Parked");

    expect(bulk).toHaveBeenCalledWith([4, 9], "parked");
    expect(bar.onApplied).toHaveBeenCalledOnce();
  });

  it("readies every selected ticket through the bulk Open", async () => {
    const user = userEvent.setup();
    const ready = vi.spyOn(apiClient, "bulkMarkTicketsReady").mockResolvedValue([]);
    const bar = renderBar([makeTicket({ id: 4, ...PREPARING }), makeTicket({ id: 9, ...PREPARING })]);

    await chooseFromDropdown(user, "Move to", "Open");

    expect(ready).toHaveBeenCalledWith([4, 9]);
    expect(bar.onApplied).toHaveBeenCalledOnce();
  });

  it("sets every selected ticket's priority", async () => {
    const user = userEvent.setup();
    const priority = vi.spyOn(apiClient, "bulkSetTicketPriority").mockResolvedValue([]);
    const bar = renderBar([makeTicket({ id: 4 }), makeTicket({ id: 9 })]);

    await chooseFromDropdown(user, "Priority", "urgent");

    expect(priority).toHaveBeenCalledWith([4, 9], "urgent");
    expect(bar.onApplied).toHaveBeenCalledOnce();
  });

  it.each(["Done", "Cancelled"])("asks before moving to %s, naming the count", async (target) => {
    const user = userEvent.setup();
    const bulk = vi.spyOn(apiClient, "bulkSetTicketStatus").mockResolvedValue([]);
    renderBar([makeTicket({ id: 4, ...PREPARING }), makeTicket({ id: 9, ...PREPARING })]);

    await chooseFromDropdown(user, "Move to", target);
    expect(bulk).not.toHaveBeenCalled();
    const confirm = screen.getByRole("group", { name: "Confirm move" });
    expect(confirm).toHaveTextContent(`Move 2 tickets to ${target}?`);

    await user.click(within(confirm).getByRole("button", { name: `Move 2 to ${target}` }));

    expect(bulk).toHaveBeenCalledWith([4, 9], target.toLowerCase());
  });

  it("moves nothing when the confirmation is turned down", async () => {
    const user = userEvent.setup();
    const bulk = vi.spyOn(apiClient, "bulkSetTicketStatus").mockResolvedValue([]);
    renderBar([makeTicket({ id: 4, ...PREPARING })]);

    await chooseFromDropdown(user, "Move to", "Done");
    await user.click(screen.getByRole("button", { name: "Don't move" }));

    expect(screen.queryByRole("group", { name: "Confirm move" })).not.toBeInTheDocument();
    expect(bulk).not.toHaveBeenCalled();
  });

  it("shows each refused ticket and its reason, and offers to deselect them", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkMarkTicketsReady").mockRejectedValue(
      new BulkRefusedError(409, "2 ticket(s) refused; nothing was changed.", [
        { id: 4, status: "open", message: "#4 open: ticket is not preparing" },
        { id: 7, status: "in_progress", message: "#7 in progress: ticket is not preparing" },
      ]),
    );
    const selected = [4, 7, 9].map((id) => makeTicket({ id, ...PREPARING }));
    const bar = renderBar(selected);

    await chooseFromDropdown(user, "Move to", "Open");

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("2 ticket(s) refused; nothing was changed.");
    expect(within(alert).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "#4 open: ticket is not preparing",
      "#7 in progress: ticket is not preparing",
    ]);
    expect(bar.onRefused).toHaveBeenCalledOnce();
    expect(bar.onApplied).not.toHaveBeenCalled();

    await user.click(within(alert).getByRole("button", { name: "Deselect the 2 that can't make this change" }));

    expect(bar.onDeselect).toHaveBeenCalledWith([4, 7]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The refresh after a refusal can drop a refused ticket (it's gone), and
  // the person can deselect one by hand: the offer counts only the rest.
  it("offers to deselect only the refused tickets still selected", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkSetTicketPriority").mockRejectedValue(
      new BulkRefusedError(404, "2 ticket(s) refused; nothing was changed.", [
        { id: 4, status: null, message: "#4: ticket not found" },
        { id: 7, status: "open", message: "#7 open: something" },
      ]),
    );
    const bar = renderBar([4, 7, 9].map((id) => makeTicket({ id })));

    await chooseFromDropdown(user, "Priority", "high");
    bar.rerender([7, 9].map((id) => makeTicket({ id })));
    await user.click(screen.getByRole("button", { name: "Deselect the 1 that can't make this change" }));

    expect(bar.onDeselect).toHaveBeenCalledWith([7]);
  });

  it("shows the server's message when it refuses the request outright", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkSetTicketPriority").mockRejectedValue(
      new ApiError(422, "ticket: too many tickets in one action: 140, at most 100"),
    );
    const bar = renderBar([makeTicket({ id: 1 })]);

    await chooseFromDropdown(user, "Priority", "low");

    expect(screen.getByRole("alert")).toHaveTextContent("ticket: too many tickets in one action: 140, at most 100");
    expect(bar.onApplied).not.toHaveBeenCalled();
    expect(bar.onRefused).not.toHaveBeenCalled();
  });

  it("leaves select mode from its own Cancel", async () => {
    const user = userEvent.setup();
    const bar = renderBar([]);

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(bar.onExit).toHaveBeenCalledOnce();
  });
});
