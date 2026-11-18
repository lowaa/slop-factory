import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type EvalSet } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import EvalSetsPage from "./eval_sets_page";

afterEach(() => vi.restoreAllMocks());

const BASE = { description: "", created_by: null, created_at: "2026-10-04T12:00:00Z", updated_at: "2026-10-04T12:00:00Z" };
const FILTER_SET: EvalSet = { ...BASE, id: "s1", name: "Logins", filter: { statuses: ["done"], q: "login" }, ticket_ids: [] };
const PINNED: EvalSet = { ...BASE, id: "s2", name: "Pinned", filter: null, ticket_ids: [4, 5] };

function stub(sets: EvalSet[]) {
  vi.spyOn(apiClient, "listEvalSets").mockResolvedValue(sets);
  vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([
    { id: "v1", name: "Careful", version: 2, harness: "claude" },
  ]);
  vi.spyOn(apiClient, "replayBatchLimits").mockResolvedValue({ max_attempts: 20, max_per_ticket: 5 });
  vi.spyOn(apiClient, "listEvalSetBatches").mockResolvedValue([]);
}

describe("EvalSetsPage", () => {
  it("lists the sets with their filter or pinned count", async () => {
    stub([FILTER_SET, PINNED]);
    await renderSignedIn(<EvalSetsPage />);

    const items = await screen.findAllByRole("listitem");
    expect(items[0]).toHaveTextContent("Logins");
    expect(items[0]).toHaveTextContent("status done; matching “login”");
    expect(items[1]).toHaveTextContent("2 pinned");
  });

  it("creates a set from a filter", async () => {
    stub([]);
    const create = vi.spyOn(apiClient, "createEvalSet").mockResolvedValue(FILTER_SET);
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "New eval set" }));
    await userEvent.type(screen.getByLabelText("Name"), "Logins");
    await userEvent.type(screen.getByLabelText("Text"), "login");
    await userEvent.click(screen.getByRole("button", { name: "Create set" }));

    expect(create).toHaveBeenCalledWith({
      name: "Logins",
      description: "",
      filter: { statuses: ["done"], q: "login" },
    });
  });

  it("shows the server's refusal of a taken name and keeps the form", async () => {
    stub([]);
    vi.spyOn(apiClient, "createEvalSet").mockRejectedValue(new ApiError(409, "that name is taken"));
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "New eval set" }));
    await userEvent.type(screen.getByLabelText("Name"), "Logins");
    await userEvent.click(screen.getByRole("button", { name: "Create set" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("that name is taken");
    expect(screen.getByLabelText("Name")).toHaveValue("Logins");
  });

  it("pins the current matches of a filter set, and offers no run until then", async () => {
    stub([FILTER_SET]);
    const pin = vi.spyOn(apiClient, "pinEvalSetMatches").mockResolvedValue({ ...FILTER_SET, filter: null, ticket_ids: [1] });
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Logins" }));
    expect(screen.queryByRole("button", { name: /Run variant/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Pin current matches" }));

    await waitFor(() => expect(pin).toHaveBeenCalledWith("s1"));
  });

  it("deletes a set only after a second click", async () => {
    stub([PINNED]);
    const del = vi.spyOn(apiClient, "deleteEvalSet").mockResolvedValue();
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Pinned" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(del).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: /Delete “Pinned”/ }));

    await waitFor(() => expect(del).toHaveBeenCalledWith("s2"));
  });

  it("runs a variant across a pinned set only after the count is confirmed, listing refused tickets", async () => {
    stub([PINNED]);
    const run = vi.spyOn(apiClient, "runEvalSet").mockResolvedValue({
      batch_id: "b1",
      added: [{ ticket_id: 4, attempts: [2, 3] }],
      refused: [{ ticket_id: 5, reason: "not replayable" }],
    });
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Pinned" }));
    await userEvent.click(screen.getByRole("button", { name: "Run variant…" }));
    expect(await screen.findByText(/at most 20/)).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText(/Careful v2/));
    await userEvent.click(screen.getByRole("button", { name: /^Review 4 replays/ }));
    expect(run).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: /Confirm: add 4 replays/ }));

    await waitFor(() => expect(run).toHaveBeenCalledWith("s2", [null, "v1"], 1, 4));
    expect(await screen.findByText(/#5: not replayable/)).toBeInTheDocument();
    await waitFor(() => expect(apiClient.listEvalSetBatches).toHaveBeenCalledTimes(2));
  });

  it("lists the batches run by variant and date", async () => {
    stub([PINNED]);
    vi.spyOn(apiClient, "listEvalSetBatches").mockResolvedValue([
      { batch_id: "b1", created_at: "2026-10-05T12:00:00Z", created_by: null, runs: 2, ticket_count: 2, version_ids: [null, "v1"] },
    ]);
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Pinned" }));
    // The generated type says string[]; the default variant is a null in it.
    const history = await screen.findByRole("region", { name: "Batch history" });
    const item = await within(history).findByRole("listitem");
    expect(item).toHaveTextContent("Default, Careful v2 — 2 runs on 2 tickets");
  });

  it("edits a set with its fields filled in, and keeps the date limits it has no field for", async () => {
    const dated: EvalSet = {
      ...FILTER_SET,
      filter: { statuses: ["done"], q: "login", created_from: "2026-01-01", updated_to: "2026-02-01" },
    };
    stub([dated]);
    const replace = vi.spyOn(apiClient, "replaceEvalSet").mockResolvedValue(dated);
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Logins" }));
    expect(within(screen.getByRole("region", { name: "Logins" })).getByText(/created 2026-01-01 to …; updated … to 2026-02-01/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText("Name")).toHaveValue("Logins");
    expect(screen.getByLabelText("Text")).toHaveValue("login");
    await userEvent.type(screen.getByLabelText("Name"), " 2");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(replace).toHaveBeenCalledWith("s1", {
        name: "Logins 2",
        description: "",
        filter: { statuses: ["done"], q: "login", created_from: "2026-01-01", updated_to: "2026-02-01" },
      }),
    );
    await waitFor(() => expect(screen.queryByRole("form", { name: "Edit eval set" })).not.toBeInTheDocument());
  });

  it("keeps the editor and its text when a save is refused", async () => {
    stub([PINNED]);
    vi.spyOn(apiClient, "replaceEvalSet").mockRejectedValue(new ApiError(409, "that name is taken"));
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Pinned" }));
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("that name is taken");
    expect(screen.getByLabelText("Name")).toHaveValue("Pinnedx");
  });

  it("creates a pinned set from typed ticket numbers, and refuses a typo", async () => {
    stub([]);
    const create = vi.spyOn(apiClient, "createEvalSet").mockResolvedValue(PINNED);
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "New eval set" }));
    await userEvent.type(screen.getByLabelText("Name"), "Mine");
    await userEvent.click(screen.getByLabelText("Pinned ticket numbers"));
    await userEvent.type(screen.getByLabelText("Ticket numbers"), "#12, 13 12 x4");
    await userEvent.click(screen.getByRole("button", { name: "Create set" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Tickets are numbers");
    expect(create).not.toHaveBeenCalled();

    await userEvent.clear(screen.getByLabelText("Ticket numbers"));
    await userEvent.type(screen.getByLabelText("Ticket numbers"), "#12, 13 12");
    await userEvent.click(screen.getByRole("button", { name: "Create set" }));
    await waitFor(() => expect(create).toHaveBeenCalledWith({ name: "Mine", description: "", ticket_ids: [12, 13] }));
  });

  it("shows a pinned set's run action once its matches are pinned, and drops a deleted set", async () => {
    const list = vi.spyOn(apiClient, "listEvalSets");
    stub([FILTER_SET]);
    list.mockResolvedValueOnce([FILTER_SET]).mockResolvedValueOnce([{ ...FILTER_SET, filter: null, ticket_ids: [1] }]).mockResolvedValue([]);
    vi.spyOn(apiClient, "pinEvalSetMatches").mockResolvedValue({ ...FILTER_SET, filter: null, ticket_ids: [1] });
    vi.spyOn(apiClient, "deleteEvalSet").mockResolvedValue();
    await renderSignedIn(<EvalSetsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Logins" }));
    await userEvent.click(screen.getByRole("button", { name: "Pin current matches" }));
    expect(await screen.findByRole("button", { name: "Run variant…" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(screen.getByRole("button", { name: /Delete “Logins”/ }));
    expect(await screen.findByText("No eval sets yet.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Logins" })).not.toBeInTheDocument();
  });
});
