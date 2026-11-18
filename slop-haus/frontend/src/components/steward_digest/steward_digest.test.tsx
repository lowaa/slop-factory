import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  apiClient,
  ApiError,
  type StewardDigest,
  type StewardNotices,
} from "@/api";
import { makeProposal } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { discussions, EDITED_DRAFT, NO_DISCUSSIONS } from "./fixtures";
import StewardDigestPage from "./steward_digest_page";
import StewardNotices_ from "./steward_notices";

afterEach(() => {
  vi.restoreAllMocks();
});

const row = {
  category: "ci_green",
  assessed: 4,
  applied: 2,
  vetoed: 1,
  undone: 0,
  needs_human: 1,
  awaiting: 0,
  unclassified: 0,
  agreed: 3,
  disagreed: 1,
  agreement_rate: 0.75 as number | null,
};

function digest(over: Partial<StewardDigest> = {}): StewardDigest {
  return {
    since: "2026-01-01T00:00:00Z",
    until: "2026-01-02T00:00:00Z",
    total: { ...row, category: "all" },
    categories: [row],
    decisions: [
      {
        proposal_id: "p1",
        ticket_id: 7,
        category: "ci_green",
        outcome: "needs_human",
        at: "2026-01-01T10:00:00Z",
      },
    ],
    decisions_cut: false,
    discussions: NO_DISCUSSIONS,
    ...over,
  };
}

function notices(over: Partial<StewardNotices> = {}): StewardNotices {
  return {
    mode: "live",
    pending: [],
    applied: [],
    cap_reached: false,
    daily_cap: 50,
    ...over,
  };
}

describe("StewardDigestPage", () => {
  it("shows the counts, the agreement rate and a link to each ticket", async () => {
    const spy = vi
      .spyOn(apiClient, "getStewardDigest")
      .mockResolvedValue(digest());
    await renderSignedIn(<StewardDigestPage />);

    expect(
      await screen.findByRole("row", { name: /ci_green/ }),
    ).toHaveTextContent("75%");
    expect(spy).toHaveBeenCalledWith("24h");
    expect(screen.getByRole("link", { name: "#7" })).toBeVisible();
    expect(screen.getByText(/needs human/)).toBeVisible();
  });

  it("shows no rate for a category with no decided proposal", async () => {
    vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(
      digest({
        categories: [{ ...row, agreement_rate: null, agreed: 0, disagreed: 0 }],
      }),
    );
    await renderSignedIn(<StewardDigestPage />);
    expect(
      await screen.findByRole("row", { name: /ci_green/ }),
    ).toHaveTextContent("–");
  });

  it("says so in words when no discussion was decided", async () => {
    vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(digest());
    await renderSignedIn(<StewardDigestPage />);
    expect(
      await screen.findByText("No decided discussions in this period."),
    ).toBeVisible();
  });

  it("shows the discussion agreement with counts on hover, warnings and the disagreements", async () => {
    vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(
      digest({
        discussions: discussions({ answer_disagreements: [EDITED_DRAFT] }),
      }),
    );
    await renderSignedIn(<StewardDigestPage />);

    const table = await screen.findByRole("table", { name: "Label" });
    const yours = within(table).getByRole("row", { name: /yours/ });
    expect(yours).toHaveTextContent("50%");
    expect(within(yours).getByText("50%")).toHaveAttribute(
      "title",
      "1 agreed, 1 disagreed",
    );
    expect(yours).toHaveTextContent("too few decisions to rely on");
    expect(within(table).getByRole("row", { name: /All/ })).toHaveTextContent(
      "67%",
    );
    expect(
      screen.getByText("Posted: Continue, but wait for the migration first."),
    ).toBeVisible();
  });

  it("still shows the discussions when the steward assessed no proposal", async () => {
    vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(
      digest({
        total: { ...row, category: "all", assessed: 0 },
        categories: [],
        decisions: [],
        discussions: discussions(),
      }),
    );
    await renderSignedIn(<StewardDigestPage />);
    expect(
      await screen.findByText("The steward assessed nothing in this period."),
    ).toBeVisible();
    expect(screen.getByRole("table", { name: "Answer" })).toBeVisible();
  });

  it("asks for seven days when chosen", async () => {
    const spy = vi
      .spyOn(apiClient, "getStewardDigest")
      .mockResolvedValue(digest());
    await renderSignedIn(<StewardDigestPage />);
    await screen.findByRole("row", { name: /ci_green/ });

    await userEvent.click(screen.getByRole("button", { name: "Last 7 days" }));

    await waitFor(() => expect(spy).toHaveBeenLastCalledWith("7d"));
  });

  it("says so when the steward assessed nothing", async () => {
    vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(
      digest({ total: { ...row, assessed: 0 }, categories: [], decisions: [] }),
    );
    await renderSignedIn(<StewardDigestPage />);
    expect(
      await screen.findByText("The steward assessed nothing in this period."),
    ).toBeVisible();
  });
});

describe("StewardNotices", () => {
  const pending = {
    proposal_id: "p1",
    ticket_id: 7,
    category: "ci_green",
    at: new Date(Date.now() + 25 * 60_000).toISOString(),
  };

  it("shows nothing when the steward is not live", async () => {
    vi.spyOn(apiClient, "getStewardNotices").mockResolvedValue(
      notices({ mode: "shadow", pending: [pending] }),
    );
    const { container } = await renderSignedIn(<StewardNotices_ />);
    await waitFor(() => expect(apiClient.getStewardNotices).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("vetoes a decision waiting out its window, then reloads", async () => {
    const get = vi
      .spyOn(apiClient, "getStewardNotices")
      .mockResolvedValueOnce(notices({ pending: [pending] }))
      .mockResolvedValue(notices());
    const veto = vi
      .spyOn(apiClient, "vetoStewardDecision")
      .mockResolvedValue(makeProposal({ id: "p1" }));
    await renderSignedIn(<StewardNotices_ />);

    expect(
      await screen.findByText(/will approve the ci_green proposal on/),
    ).toHaveTextContent("in 25 min");
    await userEvent.click(screen.getByRole("button", { name: "Veto" }));

    expect(veto).toHaveBeenCalledWith("p1");
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Veto" })).toBeNull(),
    );
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("undoes a decision applied in the last hour", async () => {
    vi.spyOn(apiClient, "getStewardNotices").mockResolvedValue(
      notices({
        applied: [
          {
            proposal_id: "p2",
            ticket_id: null,
            category: "ci_green",
            at: new Date().toISOString(),
          },
        ],
      }),
    );
    const undo = vi
      .spyOn(apiClient, "undoStewardDecision")
      .mockResolvedValue(makeProposal({ id: "p2" }));
    await renderSignedIn(<StewardNotices_ />);

    await userEvent.click(await screen.findByRole("button", { name: "Undo" }));

    expect(undo).toHaveBeenCalledWith("p2");
  });

  it("reports a refused veto", async () => {
    vi.spyOn(apiClient, "getStewardNotices").mockResolvedValue(
      notices({ pending: [pending] }),
    );
    vi.spyOn(apiClient, "vetoStewardDecision").mockRejectedValue(
      new ApiError(403, "only a tenant owner can veto"),
    );
    await renderSignedIn(<StewardNotices_ />);

    await userEvent.click(await screen.findByRole("button", { name: "Veto" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "only a tenant owner can veto",
    );
  });

  it("says when the daily cap is reached", async () => {
    vi.spyOn(apiClient, "getStewardNotices").mockResolvedValue(
      notices({ cap_reached: true, daily_cap: 3 }),
    );
    await renderSignedIn(<StewardNotices_ />);
    expect(await screen.findByRole("status")).toHaveTextContent(
      "daily cap of 3",
    );
  });
});
