import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import ReplayFollowUp from "./replay_follow_up";

const PR = "https://example.test/org/repo/pull/12";
const ticket = makeTicket({ id: 7, title: "Do the thing", body: "Original spec", status: "done" });

describe("ReplayFollowUp", () => {
  it("files a preparing ticket linking the replay's PR and branch, with no base", async () => {
    const create = vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 99 }));
    render(<ReplayFollowUp ticket={ticket} attempt={makeAttempt({ number: 2, replay: true, shadow: true, status: "closed", pr_url: PR })} />);

    await userEvent.click(screen.getByRole("button", { name: "Start a follow-up ticket" }));
    expect(screen.getByLabelText("Title")).toHaveValue("Follow-up to #7: Do the thing");
    await userEvent.click(screen.getByRole("button", { name: "File in preparing" }));

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const input = create.mock.calls[0][0];
    expect(input.status).toBe("preparing");
    expect(input.base_ref).toBeUndefined();
    expect(input.body).toContain(PR);
    expect(input.body).toContain("ticket-7-a2");
    expect(input.body).toContain("Original spec");
    expect(await screen.findByRole("link", { name: "#99" })).toHaveAttribute("href", "/tickets/99");
  });

  it("files what the person edited", async () => {
    const create = vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 99 }));
    render(<ReplayFollowUp ticket={ticket} attempt={makeAttempt({ number: 2, replay: true, status: "review", pr_url: PR })} />);

    await userEvent.click(screen.getByRole("button", { name: "Start a follow-up ticket" }));
    const title = screen.getByLabelText("Title");
    await userEvent.clear(title);
    await userEvent.type(title, "Better");
    const body = screen.getByLabelText("Description");
    await userEvent.clear(body);
    await userEvent.type(body, "My own words");
    await userEvent.click(screen.getByRole("button", { name: "File in preparing" }));

    await waitFor(() => expect(create).toHaveBeenCalled());
    expect(create.mock.calls.at(-1)![0]).toMatchObject({ title: "Better", body: "My own words" });
  });

  it("stays a draft and says why when the ticket is refused", async () => {
    vi.spyOn(apiClient, "createTicket").mockRejectedValue(new ApiError(422, "title is required"));
    render(<ReplayFollowUp ticket={ticket} attempt={makeAttempt({ number: 2, replay: true, status: "review", pr_url: PR })} />);

    await userEvent.click(screen.getByRole("button", { name: "Start a follow-up ticket" }));
    await userEvent.click(screen.getByRole("button", { name: "File in preparing" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("title is required");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "File in preparing" })).toBeEnabled();
  });

  it.each([
    ["an ordinary attempt", { replay: false, status: "review" as const, pr_url: PR }],
    ["a replay still running", { replay: true, status: "in_progress" as const, pr_url: PR }],
    ["a replay with no PR", { replay: true, status: "review" as const, pr_url: null }],
  ])("offers nothing for %s", (_n, over) => {
    render(<ReplayFollowUp ticket={ticket} attempt={makeAttempt(over)} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
