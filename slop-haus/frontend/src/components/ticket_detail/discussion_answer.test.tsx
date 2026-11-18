import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type DiscussionDraft, type DiscussionLabel, type TicketAttempt } from "@/api";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import { PENDING_POLL_MS } from "./use_discussion_answer";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

const TEMPLATE = "Merge conflicts are resolved; carry on and merge.";
const ROUND = { ticket_id: 5, attempt: 1, round: 2 };

function attempt(discussion: NonNullable<TicketAttempt["discussion"]> | null): TicketAttempt {
  return makeAttempt({ status: "discussion", discussion });
}

function stubTicket(a: TicketAttempt) {
  vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "discussion", attempts: [a] }));
  vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
  vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
}

function label(overrides: Partial<DiscussionLabel> = {}): DiscussionLabel {
  return {
    label: "routine",
    category: "merge_continue",
    reason: "The worker only asks whether to continue.",
    evidence: [
      { check: "no_open_conflicts", ok: true, ref: "abc123", detail: "clean" },
      { check: "checks_green", ok: true, ref: "pipeline 9", detail: "" },
    ],
    ticket_revision: 3,
    head_sha: "a".repeat(40),
    assessed_at: "2026-10-30T10:00:00Z",
    ...overrides,
  };
}

function draft(l: DiscussionLabel = label()): DiscussionDraft {
  return { body: l.label === "routine" ? TEMPLATE : "", label: l };
}

const routineAttempt = () =>
  attempt({ round: 2, label: "routine", category: "merge_continue", reason: "The worker only asks whether to continue." });

async function openDraft(user: ReturnType<typeof userEvent.setup>) {
  render(<TicketDetail ticketId={5} />);
  await user.click(await screen.findByRole("button", { name: "Answer with steward" }));
}

describe("DiscussionAnswer", () => {
  stubTicketDetailDefaults();

  it("no label: still shows the widget, badged Not labelled yet, with Assess now", async () => {
    const user = userEvent.setup();
    stubTicket(attempt({ round: 2, label: null }));
    // The server has no draft for a round without a label row.
    const get = vi.spyOn(apiClient, "getDiscussionDraft").mockRejectedValue(new ApiError(404, "no label"));
    vi.spyOn(apiClient, "assessDiscussionNow").mockResolvedValue(draft());
    render(<TicketDetail ticketId={5} />);

    const region = await screen.findByRole("region", { name: "Answer this discussion, attempt 1" });
    expect(region).toHaveTextContent("Not labelled yet");
    await user.click(screen.getByRole("button", { name: "Answer with steward" }));
    await user.click(await screen.findByRole("button", { name: "Assess now" }));

    expect(get).not.toHaveBeenCalled();
    expect(apiClient.assessDiscussionNow).toHaveBeenCalledWith(ROUND);
    expect(await screen.findByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("the two entry buttons are tonal and Approve is the only primary, on a routine draft", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());
    await openDraft(user);

    expect(screen.getByRole("button", { name: "Answer with steward" })).toHaveClass("button--tonal");
    expect(screen.getByRole("button", { name: "Discuss in Remote Control" })).toHaveClass("button--tonal");
    expect(await screen.findByRole("button", { name: "Approve" })).toHaveClass("button--primary");
    expect(screen.getByRole("button", { name: "Edit" })).toHaveClass("button--secondary");
    expect(screen.getByRole("button", { name: "Reject" })).toHaveClass("button--secondary");
    expect(screen.getByRole("region", { name: /Answer this discussion/ }).querySelectorAll(".button--primary")).toHaveLength(1);
  });

  it("offers no primary button on a round that is yours", async () => {
    const user = userEvent.setup();
    const yours = label({ label: "yours", category: "", reason: "A direction call." });
    stubTicket(attempt({ round: 2, label: "yours", reason: "A direction call." }));
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft(yours));
    await openDraft(user);

    await screen.findByRole("button", { name: "Edit" });
    expect(screen.getByRole("region", { name: /Answer this discussion/ }).querySelector(".button--primary")).toBeNull();
  });

  it("routine: badge, templated answer, every check, and Approve", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    const get = vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());
    vi.spyOn(apiClient, "approveDiscussionDraft").mockResolvedValue({
      action: "approve", created_at: "", final_body: TEMPLATE, original_body: TEMPLATE, reason: "",
    });

    render(<TicketDetail ticketId={5} />);
    const region = await screen.findByRole("region", { name: "Answer this discussion, attempt 1" });
    expect(region).toHaveTextContent("Routine: merge_continue");
    await user.click(screen.getByRole("button", { name: "Answer with steward" }));

    expect(get).toHaveBeenCalledWith(ROUND);
    expect(await screen.findByText(TEMPLATE)).toBeVisible();
    const checks = screen.getByRole("list", { name: "Checks" });
    expect(within(checks).getAllByRole("listitem")).toHaveLength(2);
    expect(checks).toHaveTextContent("abc123");
    expect(checks).toHaveTextContent("Pass");

    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect(apiClient.approveDiscussionDraft).toHaveBeenCalledWith(ROUND);
    expect(screen.queryByText(TEMPLATE)).not.toBeInTheDocument();
  });

  it("a scheduled steward answer says when it applies and can be vetoed", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    const at = "2026-10-30T18:00:00Z";
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue({
      ...draft(),
      steward_apply: { state: "scheduled", at, can_veto: true, can_undo: false },
    });
    const veto = vi.spyOn(apiClient, "vetoDiscussionApply").mockResolvedValue({
      action: "reject", created_at: "", final_body: "", original_body: TEMPLATE, reason: "",
    });

    await openDraft(user);

    expect(await screen.findByText(`Applies at ${new Date(at).toLocaleString()} unless you veto`)).toBeVisible();
    expect(screen.getByRole("button", { name: "Veto" })).toHaveClass("button--danger");
    await user.click(screen.getByRole("button", { name: "Veto" }));
    expect(veto).toHaveBeenCalledWith(ROUND);
  });

  it("an applied steward answer says so and offers no veto or undo", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue({
      ...draft(),
      steward_apply: { state: "applied", at: "2026-10-30T18:00:00Z", can_veto: false, can_undo: false },
      decision: { action: "approve", created_at: "", final_body: TEMPLATE, original_body: TEMPLATE, reason: "" },
    });

    await openDraft(user);

    expect(await screen.findByText("Applied by steward")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Veto" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /undo/i })).not.toBeInTheDocument();
  });

  it("approve moves the page on: the refetched ticket is no longer in discussion", async () => {
    const user = userEvent.setup();
    const get = vi.spyOn(apiClient, "getTicket");
    get.mockResolvedValueOnce(makeTicket({ id: 5, status: "discussion", attempts: [routineAttempt()] }));
    get.mockResolvedValue(makeTicket({ id: 5, status: "in_progress", attempts: [makeAttempt({ status: "in_progress" })] }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());
    vi.spyOn(apiClient, "approveDiscussionDraft").mockResolvedValue({
      action: "approve", created_at: "", final_body: TEMPLATE, original_body: TEMPLATE, reason: "",
    });
    await openDraft(user);
    await user.click(await screen.findByRole("button", { name: "Approve" }));

    await waitFor(() => expect(screen.queryByRole("region", { name: "Answer this discussion, attempt 1" })).not.toBeInTheDocument());
  });

  it("yours: shows the reason and failed check, with no Approve", async () => {
    const user = userEvent.setup();
    stubTicket(attempt({ round: 2, label: "yours", reason: "A direction call." }));
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(
      draft(label({ label: "yours", category: "", reason: "A direction call.", evidence: [{ check: "checks_green", ok: false, ref: "pipeline 9", detail: "red" }] })),
    );
    await openDraft(user);

    expect(screen.getByRole("region", { name: "Answer this discussion, attempt 1" })).toHaveTextContent("Yours: judgment");
    expect(await screen.findByRole("list", { name: "Checks" })).toHaveTextContent("Fail");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Assess now" })).not.toBeInTheDocument();
  });

  it("not assessed: offers Assess now, which shows the new draft", async () => {
    const user = userEvent.setup();
    stubTicket(attempt({ round: 2, label: "not_assessed", reason: "Daily cap reached." }));
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft(label({ label: "not_assessed", category: "", reason: "Daily cap reached.", evidence: [] })));
    vi.spyOn(apiClient, "assessDiscussionNow").mockResolvedValue(draft());
    await openDraft(user);

    expect(screen.getByRole("region", { name: "Answer this discussion, attempt 1" })).toHaveTextContent("Not assessed");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Assess now" }));

    expect(apiClient.assessDiscussionNow).toHaveBeenCalledWith(ROUND);
    expect(await screen.findByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("stale: a 409 shows the reason and refreshes the draft", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    const get = vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValueOnce(draft()).mockResolvedValue({ ...draft(), body: "Fresh template." });
    vi.spyOn(apiClient, "approveDiscussionDraft").mockRejectedValue(new ApiError(409, "the draft is stale"));
    await openDraft(user);
    await user.click(await screen.findByRole("button", { name: "Approve" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("the draft is stale");
    expect(get).toHaveBeenCalledTimes(2);
    expect(await screen.findByText("Fresh template.")).toBeVisible();
    expect(screen.queryByText(TEMPLATE)).not.toBeInTheDocument();
  });

  it("edit: posts the owner's own text", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());
    const edit = vi.spyOn(apiClient, "editDiscussionDraft").mockResolvedValue({
      action: "edit", created_at: "", final_body: "Wait for infra.", original_body: TEMPLATE, reason: "",
    });
    await openDraft(user);
    await user.click(await screen.findByRole("button", { name: "Edit" }));

    const box = screen.getByRole("textbox", { name: "Your answer" });
    expect(box).toHaveValue(TEMPLATE);
    await user.clear(box);
    await user.type(box, "Wait for infra.");
    await user.click(screen.getByRole("button", { name: "Post my answer" }));

    expect(edit).toHaveBeenCalledWith(ROUND, "Wait for infra.");
  });

  it("reject: needs a reason and sends it", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());
    const reject = vi.spyOn(apiClient, "rejectDiscussionDraft").mockResolvedValue({
      action: "reject", created_at: "", final_body: "", original_body: TEMPLATE, reason: "wrong",
    });
    await openDraft(user);
    await user.click(await screen.findByRole("button", { name: "Reject" }));

    expect(screen.getByRole("button", { name: "Reject draft" })).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "Reason" }), "not routine");
    await user.click(screen.getByRole("button", { name: "Reject draft" }));

    expect(reject).toHaveBeenCalledWith(ROUND, "not routine");
  });

  it("offers no decision on a round that is pending, and none once decided", async () => {
    const user = userEvent.setup();
    stubTicket(attempt({ round: 2, label: "pending" }));
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValueOnce(draft(label({ label: "pending", evidence: [] })));
    await openDraft(user);
    expect(screen.getByRole("region", { name: "Answer this discussion, attempt 1" })).toHaveTextContent("Assessing…");
    await screen.findByText("The worker only asks whether to continue.");
    for (const name of ["Approve", "Edit", "Reject", "Assess now"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("an open pending panel refetches the draft once the steward's label completes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      stubTicket(attempt({ round: 2, label: "pending" }));
      const get = vi
        .spyOn(apiClient, "getDiscussionDraft")
        .mockResolvedValueOnce(draft(label({ label: "pending", evidence: [] })))
        .mockResolvedValue(draft());
      await openDraft(user);
      await screen.findByText("The worker only asks whether to continue.");
      expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();

      stubTicket(routineAttempt());
      await vi.advanceTimersByTimeAsync(PENDING_POLL_MS);

      expect(await screen.findByRole("button", { name: "Approve" })).toBeVisible();
      expect(get).toHaveBeenCalledTimes(2);

      // Polling stops once the label is no longer pending.
      const reads = vi.mocked(apiClient.getTicket).mock.calls.length;
      await vi.advanceTimersByTimeAsync(PENDING_POLL_MS * 3);
      expect(apiClient.getTicket).toHaveBeenCalledTimes(reads);
      expect(get).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("offers nothing on a round already decided", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue({
      ...draft(),
      decision: { action: "approve", created_at: "", final_body: TEMPLATE, original_body: TEMPLATE, reason: "" },
    });
    await openDraft(user);
    expect(await screen.findByText(/decided: approve/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
  });

  it("shows a failed load and keeps the panel retryable", async () => {
    const user = userEvent.setup();
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "getDiscussionDraft").mockRejectedValueOnce(new ApiError(403, "only an owner")).mockResolvedValue(draft());
    await openDraft(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("only an owner");
    await user.click(screen.getByRole("button", { name: "Answer with steward" }));
    await user.click(screen.getByRole("button", { name: "Answer with steward" }));
    expect(await screen.findByText(TEMPLATE)).toBeVisible();
  });

  it("with several attempts, answers on the discussing attempt's own tab with its round", async () => {
    const user = userEvent.setup();
    const second = makeAttempt({ number: 2, status: "discussion", discussion: { round: 4, label: "routine", category: "merge_continue", reason: "r" } });
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "discussion", attempts: [makeAttempt({ number: 1, status: "in_progress" }), second] }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    const get = vi.spyOn(apiClient, "getDiscussionDraft").mockResolvedValue(draft());

    render(<TicketDetail ticketId={5} initialAttempt={2} />);
    await user.click(await screen.findByRole("button", { name: "Answer with steward" }));

    expect(get).toHaveBeenCalledWith({ ticket_id: 5, attempt: 2, round: 4 });
  });

  // Navigation itself is covered by discuss_shortcut.test.tsx, which has a router.
  it("Discuss in Remote Control sits in the widget's row, once", async () => {
    stubTicket(routineAttempt());
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
    render(<TicketDetail ticketId={5} />);

    const discuss = await screen.findByRole("button", { name: "Discuss in Remote Control" });
    await waitFor(() => expect(discuss).toBeEnabled());
    const answer = screen.getByRole("button", { name: "Answer with steward" });
    expect(discuss.parentElement).toBe(answer.parentElement);
    expect(screen.getAllByRole("button", { name: "Discuss in Remote Control" })).toHaveLength(1);
  });
});
