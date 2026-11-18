import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile, makeProposal, makeTicket } from "@/test/fixtures";
import { FILTER_STATUSES } from "@/domain/ticket_filter_model";
import { chooseFromDropdown } from "@/test/widgets";
import { renderApp, mockBoardShell } from "@/test/render_app";

describe("approvals tab", () => {
  const proposal = () => makeProposal({ id: "a", ticket_id: 7, ticket_title: "Pick a database" });
  const filterOf = () => vi.mocked(apiClient.listPendingProposalsPage).mock.calls.at(-1)?.[0];

  function setup(path: string) {
    window.history.pushState({}, "", path);
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([proposal()]);
    vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: [proposal()] });
  }

  it("hides parked, cancelled and done by default, leaving the address plain", async () => {
    setup("/approvals");
    renderApp();

    await screen.findByText(/Pick a database/);
    expect(filterOf()!.status).toEqual(FILTER_STATUSES.filter((st) => !["parked", "cancelled", "done"].includes(st)));
    expect(window.location.search).toBe("");
  });

  it("reads the filter from the address and keeps a changed one there", async () => {
    setup("/approvals?q=db&exclude=cancelled,done");
    const user = userEvent.setup();
    renderApp();

    await screen.findByText(/Pick a database/);
    expect(filterOf()).toEqual(expect.objectContaining({ q: "db", status: expect.arrayContaining(["parked"]) }));

    await chooseFromDropdown(user, "Priority", "urgent");
    await waitFor(() => expect(window.location.search).toContain("priority=urgent"));
    await waitFor(() => expect(filterOf()).toEqual(expect.objectContaining({ priority: ["urgent"] })));
  });

  it("is kept by a ticket's Back link", async () => {
    setup("/approvals?q=db");
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Pick a database" }));
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("link", { name: /Pick a database/ }));
    await user.click(await screen.findByRole("link", { name: "← Back to approvals" }));

    await waitFor(() => expect(window.location.pathname).toBe("/approvals"));
    expect(new URLSearchParams(window.location.search).get("q")).toBe("db");
    expect(screen.getByLabelText("Title or text")).toHaveValue("db");
  });

  it("does not take another page's filter, nor give its own to it", async () => {
    setup("/blocked?q=graph");
    vi.spyOn(apiClient, "listBlockedPage").mockResolvedValue({ tickets: [] });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("link", { name: /Approvals/ }));
    await screen.findByText(/Pick a database/);
    expect(screen.getByLabelText("Title or text")).toHaveValue("");
    expect(filterOf()!.q).toBeUndefined();

    await user.type(screen.getByLabelText("Title or text"), "db");
    await waitFor(() => expect(window.location.search).toBe("?q=db"));
    await user.click(screen.getByRole("link", { name: "Blocked" }));
    await waitFor(() => expect(screen.getByLabelText("Title or text")).toHaveValue(""));
  });
});
