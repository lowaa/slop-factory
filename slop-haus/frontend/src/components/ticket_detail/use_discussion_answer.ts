import { useEffect, useRef, useState } from "react";
import { apiClient, ApiError, type DiscussionDraft, type DiscussionRound, type TicketAttempt } from "@/api";
import { invalidate, resourceKeys } from "@/lib/resource";
import { useResourceClient } from "@/lib/resource/client";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import type { EventAttrs } from "@/usage/catalog";
import { useUsage } from "@/usage/use_usage";

// A new round of the same attempt starts a fresh panel, so a draft read for
// the last round can never be decided against the next.
export function answerKey(attempt: TicketAttempt): string {
  return `${attempt.number}/${attempt.discussion?.round ?? 0}`;
}

// How often an open panel re-reads the ticket while the steward's label is
// still pending, so its completion reaches the panel without a reopen.
export const PENDING_POLL_MS = 5000;

export type AnswerMode = "view" | "edit" | "reject";

export interface DiscussionAnswer {
  open: boolean;
  // Whether the steward has labelled the round. The server has no draft to
  // give for one that is not, so none is read.
  labelled: boolean;
  draft: DiscussionDraft | null;
  mode: AnswerMode;
  // The edit text or the rejection reason being typed, by mode.
  input: string;
  action: AsyncAction;
  // Set when the server refused a decision because the draft no longer
  // matches the ticket; the draft shown is already the refreshed one.
  stale: string | null;
  toggle: () => void;
  setMode: (mode: AnswerMode) => void;
  setInput: (input: string) => void;
  assess: () => void;
  approve: () => void;
  veto: () => void;
  submit: () => void;
}

// useDiscussionAnswer is the owner's side of one attempt's discussion round:
// reading the steward's draft and deciding it. Nothing is posted by this
// panel until a decision is sent (at level 3+ the steward may post a routine
// answer itself); the server posts the answer as the owner.
export function useDiscussionAnswer(ticketId: number, attempt: TicketAttempt): DiscussionAnswer {
  const client = useResourceClient();
  const action = useAsyncAction();
  const usage = useUsage();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<DiscussionDraft | null>(null);
  const [mode, setModeState] = useState<AnswerMode>("view");
  const [input, setInput] = useState("");
  const [stale, setStale] = useState<string | null>(null);
  const round = attempt.discussion?.round;
  const labelState = attempt.discussion?.label;
  const shownLabel = useRef(labelState);

  function address(): DiscussionRound | null {
    return round === undefined ? null : { ticket_id: ticketId, attempt: attempt.number, round };
  }

  // Only the latest read may set the draft: a slow read begun before a label
  // change must not overwrite the one begun after it.
  const loadSeq = useRef(0);
  async function load(r: DiscussionRound) {
    const seq = ++loadSeq.current;
    const next = await apiClient.getDiscussionDraft(r);
    if (seq === loadSeq.current) setDraft(next);
  }

  // A 409 means the ticket moved on, the checks changed or the round was
  // already decided: show the server's reason and what it holds now.
  async function refreshAfterConflict(r: DiscussionRound, err: unknown, fallback: string) {
    setStale(errorMessage(err, fallback));
    setModeState("view");
    try {
      await load(r);
    } catch {
      // The reason is already shown; the draft stays as it was.
    }
    await invalidate(resourceKeys.ticket(ticketId), client, { restart: true });
  }

  // A decision closes the panel; assessing only replaces the draft in it.
  function decide(task: (r: DiscussionRound) => Promise<unknown>, failure: string, closes = true) {
    const r = address();
    if (!r) return;
    void action.run(async () => {
      setStale(null);
      try {
        await task(r);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          await refreshAfterConflict(r, err, failure);
          return;
        }
        throw err;
      }
      if (!closes) {
        await invalidate(resourceKeys.ticket(ticketId), client, { restart: true });
        return;
      }
      setOpen(false);
      setModeState("view");
      await invalidate(resourceKeys.ticket(ticketId), client, { restart: true });
      await invalidate(resourceKeys.ticketComments(ticketId), client);
    }, failure);
  }

  // The steward labels asynchronously: an open panel holding a draft read
  // under an older label would offer no decision after the label completes.
  useEffect(() => {
    if (shownLabel.current === labelState) return;
    shownLabel.current = labelState;
    const r = address();
    if (!open || !r) return;
    void action.run(() => load(r), "Failed to load the steward's draft.");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [labelState]);

  useEffect(() => {
    if (!open || labelState !== "pending") return;
    const timer = setInterval(() => {
      void invalidate(resourceKeys.ticket(ticketId), client);
    }, PENDING_POLL_MS);
    return () => clearInterval(timer);
  }, [open, labelState, ticketId, client]);

  function toggle() {
    const r = address();
    if (!r) return;
    if (open) {
      setOpen(false);
      return;
    }
    setOpen(true);
    usage.event("steward_answer", { action: "open" });
    setStale(null);
    if (!labelState) return;
    void action.run(() => load(r), "Failed to load the steward's draft.");
  }

  // Counted when asked, whether or not the server accepts it: a refusal is
  // still a use of the feature.
  function counted(what: EventAttrs<"steward_answer">["action"], run: () => void): () => void {
    return () => {
      usage.event("steward_answer", { action: what });
      run();
    };
  }

  return {
    open,
    labelled: !!labelState,
    draft,
    mode,
    input,
    action,
    stale,
    toggle,
    setMode: (next) => {
      setModeState(next);
      setInput(next === "edit" ? (draft?.body ?? "") : "");
    },
    setInput,
    assess: counted("assess", () =>
      decide(
        async (r) => {
          const seq = ++loadSeq.current;
          const next = await apiClient.assessDiscussionNow(r);
          if (seq === loadSeq.current) setDraft(next);
        }, "Failed to assess the discussion.",
        false,
      ),
    ),
    approve: counted("approve", () => decide((r) => apiClient.approveDiscussionDraft(r), "Failed to approve the steward's draft.")),
    veto: counted("veto", () => decide((r) => apiClient.vetoDiscussionApply(r), "Failed to veto the steward's answer.")),
    submit: () =>
      mode === "edit"
        ? counted("edit", () => decide((r) => apiClient.editDiscussionDraft(r, input.trim()), "Failed to post your answer."))()
        : counted("reject", () => decide((r) => apiClient.rejectDiscussionDraft(r, input.trim()), "Failed to reject the draft."))(),
  };
}
