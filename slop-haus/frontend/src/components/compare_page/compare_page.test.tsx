import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { defaultResourceClient } from "@/lib/resource";
import { renderSignedIn } from "@/test/render_signed_in";
import { makeCompareResult } from "./compare_fixtures";
import ComparePage from "./compare_page";

const BATCH = "0b8a2d6e-3f4c-4b0e-9a55-2f1d1d6f4c11";

afterEach(() => {
  vi.restoreAllMocks();
  defaultResourceClient.clear();
});

function stubVariants() {
  vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([
    { id: "v1", name: "Careful", version: 1, harness: "claude" },
    { id: "v2", name: "Fast", version: 1, harness: "claude" },
  ]);
}

async function pickCarefulChallenger() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("checkbox", { name: "Careful" }));
  return user;
}

describe("ComparePage", () => {
  it("asks for the chosen replay batch, baseline and challengers", async () => {
    stubVariants();
    const compare = vi.spyOn(apiClient, "compareVariants").mockResolvedValue(makeCompareResult());
    await renderSignedIn(<ComparePage />);
    const user = await pickCarefulChallenger();

    expect(screen.getByRole("button", { name: "Compare" })).toBeDisabled();
    await user.type(screen.getByLabelText("Replay batch id"), BATCH);
    await user.click(screen.getByRole("button", { name: "Compare" }));

    await screen.findByText("Careful versus Default variant");
    expect(compare).toHaveBeenCalledWith({ replay_batch_id: BATCH, baseline: "", challengers: ["Careful"] });
  });

  it("asks for the tickets the filter matches, with the size bounds", async () => {
    stubVariants();
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue({
      tickets: [{ id: 7 }, { id: 9 }] as never,
    });
    const compare = vi.spyOn(apiClient, "compareVariants").mockResolvedValue(makeCompareResult());
    await renderSignedIn(<ComparePage />);
    const user = await pickCarefulChallenger();

    await user.click(screen.getByRole("radio", { name: "Tickets picked by filter" }));
    expect(await screen.findByText("2 tickets match")).toBeVisible();
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ statuses: ["done"], limit: 200 }));
    await user.type(screen.getByLabelText("Search tickets"), "login");
    await user.type(screen.getByLabelText("Largest original change (lines)"), "50");
    // Typing swaps the list's query: Compare stays disabled until the new
    // match set has loaded, so wait for it rather than clicking into the gap.
    await waitFor(() => expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ text: "login" })));
    expect(await screen.findByText("2 tickets match")).toBeVisible();
    await waitFor(() => expect(screen.getByRole("button", { name: "Compare" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Compare" }));

    await waitFor(() =>
      expect(compare).toHaveBeenCalledWith({
        ticket_ids: [7, 9],
        max_diff_lines: 50,
        baseline: "",
        challengers: ["Careful"],
      }),
    );
  });

  it("refuses a set over the server's bound before asking", async () => {
    stubVariants();
    vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue({
      tickets: Array.from({ length: 200 }, (_, id) => ({ id: id + 1 })) as never,
      nextCursor: "more",
    });
    const compare = vi.spyOn(apiClient, "compareVariants");
    await renderSignedIn(<ComparePage />);
    const user = await pickCarefulChallenger();

    await user.click(screen.getByRole("radio", { name: "Tickets picked by filter" }));

    expect(await screen.findByText(/a comparison reads at most 200/)).toBeVisible();
    expect(screen.getByRole("button", { name: "Compare" })).toBeDisabled();
    expect(compare).not.toHaveBeenCalled();
  });

  it("sends the chosen baseline and leaves it out of the challengers", async () => {
    stubVariants();
    const compare = vi.spyOn(apiClient, "compareVariants").mockResolvedValue(makeCompareResult());
    await renderSignedIn(<ComparePage />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("checkbox", { name: "Fast" }));
    await user.click(screen.getByRole("combobox", { name: "Baseline" }));
    await user.click(await screen.findByRole("option", { name: "Careful" }));
    await user.type(screen.getByLabelText("Replay batch id"), BATCH);
    await user.click(screen.getByRole("button", { name: "Compare" }));

    await waitFor(() =>
      expect(compare).toHaveBeenCalledWith({ replay_batch_id: BATCH, baseline: "Careful", challengers: ["Fast"] }),
    );
    expect(screen.queryByRole("checkbox", { name: "Careful" })).toBeNull();
  });

  it("fetches again when asked for what is already shown", async () => {
    stubVariants();
    const compare = vi.spyOn(apiClient, "compareVariants").mockResolvedValue(makeCompareResult());
    await renderSignedIn(<ComparePage />);
    const user = await pickCarefulChallenger();
    await user.type(screen.getByLabelText("Replay batch id"), BATCH);
    await user.click(screen.getByRole("button", { name: "Compare" }));
    await screen.findByText("Careful versus Default variant");

    await user.click(screen.getByRole("button", { name: "Compare" }));

    await waitFor(() => expect(compare).toHaveBeenCalledTimes(2));
  });

  it("shows the server's failure", async () => {
    stubVariants();
    vi.spyOn(apiClient, "compareVariants").mockRejectedValue(new Error("boom"));
    await renderSignedIn(<ComparePage />);
    const user = await pickCarefulChallenger();
    await user.type(screen.getByLabelText("Replay batch id"), BATCH);
    await user.click(screen.getByRole("button", { name: "Compare" }));

    expect(await screen.findByRole("alert")).toBeVisible();
  });
});
