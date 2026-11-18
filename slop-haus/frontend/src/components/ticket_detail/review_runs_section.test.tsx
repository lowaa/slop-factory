import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type TicketAttempt } from "@/api";
import { makeAttempt, makeReviewFinding, makeReviewRun, makeTicket } from "@/test/fixtures";
import { deferred, TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";
import ReviewRunsSection from "./review_runs_section";
import { groupByHead, sameHead } from "./review_runs_model";

type Signals = NonNullable<TicketAttempt["signals"]>;

const withHead = (head_sha: string) => makeAttempt({ signals: { head_sha } as Signals });

function open(runs: ReturnType<typeof makeReviewRun>[], attempt = withHead("abcdef1")) {
  vi.spyOn(apiClient, "listAttemptReviewRuns").mockResolvedValue(runs);
  render(<ReviewRunsSection ticketId={5} attempt={attempt} />);
  return screen.findByRole("region", { name: "Reviews" });
}

describe("the Reviews section", () => {
  it("says it is loading until the runs arrive", async () => {
    const reply = deferred<ReturnType<typeof makeReviewRun>[]>();
    const list = vi.spyOn(apiClient, "listAttemptReviewRuns").mockReturnValue(reply.promise);
    render(<ReviewRunsSection ticketId={5} attempt={makeAttempt({ number: 2 })} />);

    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(5, 2);
  });

  it("has an empty state", async () => {
    const section = within(await open([]));
    expect(await section.findByText("No review has been posted for this attempt.")).toBeInTheDocument();
  });

  it("shows the server's message when the runs cannot be read", async () => {
    vi.spyOn(apiClient, "listAttemptReviewRuns").mockRejectedValue(new ApiError(500, "review store is down"));
    render(<ReviewRunsSection ticketId={5} attempt={makeAttempt()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("review store is down");
  });

  it("shows each run's personas, verdict, who ran it and its finding count", async () => {
    const section = within(
      await open([
        makeReviewRun({ id: "a", personas: ["security"], verdict: "request_changes", posted_by_kind: "agent", posted_by_name: "builder-1", findings: [makeReviewFinding()] }),
        makeReviewRun({ id: "b", personas: ["standard"], verdict: "approve", incomplete: true }),
      ]),
    );
    const security = within((await section.findByText("security")).closest("li") as HTMLElement);
    expect(security.getByText("request changes")).toBeInTheDocument();
    expect(security.getByText(/by agent builder-1/)).toBeInTheDocument();
    expect(security.getByText("1 finding")).toBeInTheDocument();
    const standard = within(section.getByText("standard").closest("li") as HTMLElement);
    expect(standard.getByText("incomplete")).toBeInTheDocument();
    expect(standard.getByText(/by supervisor home-1/)).toBeInTheDocument();
    expect(standard.getByText("0 findings")).toBeInTheDocument();
  });

  it("shows why a failed run failed", async () => {
    const section = within(
      await open([makeReviewRun({ status: "failed", verdict: "", failure: { class: "usage_limit", message: "you hit your limit" } })]),
    );
    expect(await section.findByRole("alert")).toHaveTextContent("Failed (usage_limit): you hit your limit");
    expect(section.getByText("failed")).toBeInTheDocument();
  });

  it("groups by head, newest first, and marks only the attempt's current head", async () => {
    const section = within(
      await open([
        makeReviewRun({ id: "1", head_sha: "abcdef1111111" }),
        makeReviewRun({ id: "2", head_sha: "0123456789abc" }),
        makeReviewRun({ id: "3", head_sha: "0123456789abc" }),
      ]),
    );
    const groups = await section.findAllByRole("region");
    expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual(["Head abcdef1", "Head 0123456"]);
    expect(within(groups[0]).getByText("current head")).toBeInTheDocument();
    expect(within(groups[1]).queryByText("current head")).toBeNull();
    expect(within(groups[1]).getAllByRole("listitem")).toHaveLength(2);
  });

  it("marks no head as current while the attempt has no signals", async () => {
    const section = within(await open([makeReviewRun()], makeAttempt({ signals: null })));
    await section.findByText("abcdef1");
    expect(section.queryByText("current head")).toBeNull();
  });

  it("expands a finding to its detail and label, and says when it is unlabelled", async () => {
    const user = userEvent.setup();
    const section = within(
      await open([
        makeReviewRun({
          findings: [
            makeReviewFinding({ id: "f1", summary: "Leaks a handle", detail: "Never closed.", label: { verdict: "confirmed", reason: "real", fixed_in: "9999999aaaa", labelled_at: "2026-01-03T00:00:00Z" } }),
            makeReviewFinding({ id: "f2", summary: "Naming", detail: "Rename it.", line: 0, label: null }),
          ],
        }),
      ]),
    );
    const leak = await section.findByRole("button", { name: /Leaks a handle/ });
    expect(leak).toHaveTextContent("server/a.go:12");
    expect(leak).toHaveAttribute("aria-expanded", "false");
    expect(section.queryByText("Never closed.")).toBeNull();

    await user.click(leak);
    expect(leak).toHaveAttribute("aria-expanded", "true");
    expect(section.getByText("Never closed.")).toBeVisible();
    expect(section.getByText("confirmed")).toBeInTheDocument();
    expect(section.getByText(/: real/)).toBeInTheDocument();
    expect(section.getByText("9999999")).toBeInTheDocument();

    const naming = section.getByRole("button", { name: /Naming/ });
    expect(naming).toHaveTextContent(/^majorserver\/a\.goNaming$/);
    await user.click(naming);
    expect(section.getByText("unlabelled")).toBeVisible();
  });
});

describe("on the ticket page", () => {
  stubTicketDetailDefaults();

  function openTicket(attempts: TicketAttempt[], initialAttempt?: number) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review", attempts }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    const list = vi.spyOn(apiClient, "listAttemptReviewRuns").mockResolvedValue([makeReviewRun({ summary: "On this attempt." })]);
    render(<TicketDetail ticketId={5} initialAttempt={initialAttempt} />);
    return list;
  }

  it("is on the single attempt's page, for that attempt", async () => {
    const list = openTicket([makeAttempt({ number: 1, status: "review" })]);
    expect(await screen.findByText("On this attempt.")).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(5, 1);
  });

  it("is on an attempt's tab, for that attempt", async () => {
    const list = openTicket(
      [makeAttempt({ number: 1, status: "review" }), makeAttempt({ number: 2, status: "queued", promoted: false, shadow: true })],
      2,
    );
    expect(await screen.findByText("On this attempt.")).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(5, 2);
    expect(list).not.toHaveBeenCalledWith(5, 1);
  });
});

describe("head grouping", () => {
  it("compares an abbreviated sha with a full one either way round", () => {
    expect(sameHead("abcd", "abcdef")).toBe(true);
    expect(sameHead("abcdef", "abcd")).toBe(true);
    expect(sameHead("abce", "abcdef")).toBe(false);
    expect(sameHead("a", "abcdef")).toBe(false);
    expect(sameHead("abc", "abcdef")).toBe(false);
    expect(sameHead(null, "abcdef")).toBe(false);
    expect(sameHead("", "")).toBe(false);
  });

  it("places a head where its newest run is", () => {
    const runs = [makeReviewRun({ id: "1", head_sha: "b" }), makeReviewRun({ id: "2", head_sha: "a" }), makeReviewRun({ id: "3", head_sha: "b" })];
    expect(groupByHead(runs).map((g) => [g.headSha, g.runs.map((r) => r.id)])).toEqual([
      ["b", ["1", "3"]],
      ["a", ["2"]],
    ]);
  });
});
