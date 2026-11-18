import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { apiClient, type TicketAttempt } from "@/api";
import { makeAttempt as attempt, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

type Signals = NonNullable<TicketAttempt["signals"]>;

const clean: Signals = {
  computed_at: "2026-01-01T00:00:00Z", final: true, head_sha: "abc",
  autonomy: { blocked_handbacks: 0, discussions: 1, scope_extension_denials: 0 },
  cheating: {
    assertions_removed: { files: [], known: true },
    out_of_scope: { files: [], known: true },
    skips_added: { files: [], known: true },
    tests_deleted: { files: [], known: true },
  },
  checks: { own_test_failures: [], passed: "yes", regressions: [], reruns: 0, unattributed: false },
  own_tests: [{ class: "fails-on-base", flagged: false, path: "a_test.go" }],
  size: {
    binary_files: 0,
    files_touched: 3,
    generated_lines: 0,
    generated_rules_applied: true,
    generated_skipped: 0,
    known: true,
    lines_added: 10,
    lines_removed: 2,
  },
  time: { first_claim_at: null, last_run_ended_at: null, runs: 1, turns: 12, wall_clock_seconds: 125 },
};

const unknown: Signals = {
  computed_at: "2026-01-01T00:00:00Z", final: true, head_sha: "abc",
  autonomy: { blocked_handbacks: null, discussions: null, scope_extension_denials: null },
  cheating: {
    assertions_removed: { files: [], known: false },
    out_of_scope: { files: [], known: false },
    skips_added: { files: [], known: false },
    tests_deleted: { files: [], known: false },
  },
  checks: { own_test_failures: null, passed: "unknown", regressions: null, reruns: null, unattributed: true },
  own_tests: [],
  size: { ...clean.size, known: false },
  time: { first_claim_at: null, last_run_ended_at: null, runs: null, turns: null, wall_clock_seconds: null },
};

describe("SignalsSection", () => {
  stubTicketDetailDefaults();

  async function renderSignals(signals: TicketAttempt["signals"]) {
    const ticket = makeTicket({ id: 5, attempts: [attempt({ signals })] });
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    render(<TicketDetail ticketId={ticket.id} />);
    return within(await screen.findByRole("region", { name: "Signals" }));
  }

  it("shows a clean attempt's signals with nothing flagged", async () => {
    const view = await renderSignals(clean);

    expect(view.getByText("3 files, +10 -2")).toBeInTheDocument();
    expect(view.getByText("2m 5s")).toBeInTheDocument();
    expect(view.getByText("fails on base")).toBeInTheDocument();
    expect(view.getByText("yes")).toBeInTheDocument();
    expect(view.getAllByText("none")).toHaveLength(4);
    expect(document.querySelectorAll("[data-flagged]")).toHaveLength(0);
  });

  it("flags a tripped cheating check, a regression and a new test passing on the base", async () => {
    const view = await renderSignals({
      ...clean,
      cheating: { ...clean.cheating, skips_added: { files: ["x_test.go"], known: true } },
      checks: { ...clean.checks, regressions: [{ file: "y_test.go", name: "TestY" }] },
      own_tests: [{ class: "passes-on-base", flagged: true, path: "new_test.go" }],
    });

    expect(view.getByText("⚑ x_test.go")).toBeInTheDocument();
    expect(view.getByText("⚑ 1")).toBeInTheDocument();
    expect(view.getByText("⚑ passes on base")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-flagged='true']")).toHaveLength(3);
  });

  it("flags failed checks and the attempt's own failing tests", async () => {
    const view = await renderSignals({
      ...clean,
      checks: { ...clean.checks, passed: "no", own_test_failures: [{ file: "a_test.go", name: "TestA" }] },
    });

    expect(view.getByText("⚑ no")).toBeInTheDocument();
    expect(view.getByText("⚑ 1")).toBeInTheDocument();
  });

  it("says unknown for missing data and flags nothing", async () => {
    const view = await renderSignals(unknown);

    expect(view.getAllByText("unknown").length).toBeGreaterThanOrEqual(10);
    expect(document.querySelectorAll("[data-flagged]")).toHaveLength(0);
  });

  it("says unknown when the attempt has no signals yet", async () => {
    const view = await renderSignals(null);

    expect(view.getByText("unknown")).toBeInTheDocument();
  });
});
