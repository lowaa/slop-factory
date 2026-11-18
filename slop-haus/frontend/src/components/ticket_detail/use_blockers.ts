import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { apiClient, type BlockerCandidate, type BlockerList } from "@/api";
import { useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import type { TicketStore } from "./use_ticket_store";

// CandidateState keeps "still loading" and "could not load" apart from
// "there is genuinely nothing to block this on" -- they look identical as
// an empty array, and two of the three would have the UI state as fact
// something it does not know.
export type CandidateState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "loaded"; tickets: BlockerCandidate[] };

export interface BlockerActions {
  action: AsyncAction;
  candidates: CandidateState;
  refreshCandidates: () => void;
  blockerIdInput: string;
  setBlockerIdInput: (value: string) => void;
  handleAddBlocker: (e: FormEvent<HTMLFormElement>) => void;
  handleRemoveBlocker: (blockerId: number) => void;
}

// useBlockers is the ticket's blockers and the picker for adding one. Its
// state is held by TicketDetail, which calls this, for the reason
// useReviewActions gives.
export function useBlockers(store: TicketStore, ticketId: number): BlockerActions {
  const action = useAsyncAction();
  const [blockerIdInput, setBlockerIdInput] = useState("");
  const [candidates, setCandidates] = useState<CandidateState>({ status: "loading" });
  const candidateRequest = useRef(0);

  // The candidate list is the server's answer to "what can block this",
  // refetched after every add or remove because each one changes it: the
  // added ticket drops out, and so does anything that now reaches this
  // ticket through it.
  //
  // A refetch deliberately leaves the current list on screen rather than
  // dropping back to "loading" -- the picker would blink out from under
  // the pointer after every add. The generation counter is what keeps a
  // slow earlier response from landing on top of a newer one and offering
  // a blocker that has just been added.
  const refreshCandidates = useCallback(() => {
    const generation = ++candidateRequest.current;
    apiClient
      .getBlockerCandidates(ticketId)
      .then((tickets) => {
        if (candidateRequest.current === generation) setCandidates({ status: "loaded", tickets });
      })
      .catch(() => {
        if (candidateRequest.current === generation) setCandidates({ status: "error" });
      });
  }, [ticketId]);

  // No cleanup bumping the generation here, though it looks like it wants
  // one: TicketDetail keys this page by ticket id, so ticketId never
  // changes under it, and a reply for a ticket no longer on show lands on
  // an unmounted page. The guard is only for this ticket's own refetches.
  useEffect(refreshCandidates, [refreshCandidates]);

  // changeBlockers applies an add or remove from what its response carries
  // -- server/internal/ticket/graph_handler.go's blockerListResponse is the
  // blocker list and the status, not a whole ticket. The status because
  // the change can move it: an unfinished blocker takes an open ticket to
  // blocked, and removing the last one opens it again. That makes it a
  // status write, so it takes a turn on the status generation like Ready
  // does. The server records the change as the human's comment, so the
  // comments are refetched too.
  async function changeBlockers(change: () => Promise<BlockerList>) {
    const generation = store.beginFieldWrite("status");
    const { blocked_by, blockers, status, allowed_moves, can_ready } = await change();
    const statusCurrent = store.isCurrent("status", generation);
    store.updateLoaded((prev) => ({
      ...prev,
      ticket: {
        ...prev.ticket,
        depends_on: blocked_by,
        blockers,
        ...(statusCurrent ? { status, allowed_moves, can_ready } : {}),
      },
    }));
    refreshCandidates();
    await store.refreshComments();
  }

  function handleAddBlocker(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (store.state.status !== "loaded") return;
    const blockerId = Number(blockerIdInput);
    if (!Number.isInteger(blockerId) || blockerId < 1) {
      action.setError("Choose a ticket.");
      return;
    }
    // AddBlocker refuses a cycle with a 422 -- service.go's "dependency
    // would create a cycle: #X already depends on #Y" detail is already
    // written for a human, so it's shown as-is rather than genericized.
    void action.run(async () => {
      await changeBlockers(() => apiClient.addBlocker(ticketId, blockerId));
      setBlockerIdInput("");
    }, "Failed to add blocker.");
  }

  function handleRemoveBlocker(blockerId: number) {
    if (store.state.status !== "loaded") return;
    void action.run(
      () => changeBlockers(() => apiClient.removeBlocker(ticketId, blockerId)),
      "Failed to remove blocker.",
    );
  }

  return {
    action,
    candidates,
    refreshCandidates,
    blockerIdInput,
    setBlockerIdInput,
    handleAddBlocker,
    handleRemoveBlocker,
  };
}
