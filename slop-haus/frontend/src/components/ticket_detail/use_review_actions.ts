import { useState, type FormEvent } from "react";
import { apiClient } from "@/api";
import { useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface ReviewActions {
  approve: AsyncAction;
  requestChanges: AsyncAction;
  postReview: AsyncAction;
  changesComment: string;
  setChangesComment: (comment: string) => void;
  postReviewComment: string;
  setPostReviewComment: (comment: string) => void;
  // The follow-ups filed from this page, newest last, so the human can see
  // each one landed and open it.
  followUps: number[];
  addFollowUp: (id: number) => void;
  handleApprove: () => void;
  handleRequestChanges: (e: FormEvent<HTMLFormElement>) => void;
  handleMarkReviewed: (e: FormEvent<HTMLFormElement>) => void;
}

// useReviewActions is a human's part in a ticket's review, before its
// change merges and after. Its state is held by TicketDetail, which calls
// this, rather than by ReviewPanel: the panel is only mounted while a
// ticket is loaded, and a draft lasts as long as its ticket is on show,
// not as long as the panel happens to be mounted.
//
// attempt is the attempt the page is of, when the ticket has several: an
// approval or a request for changes is then that attempt's alone.
export function useReviewActions(store: TicketStore, ticketId: number, attempt?: number): ReviewActions {
  const approve = useAsyncAction();
  const requestChanges = useAsyncAction();
  const postReview = useAsyncAction();
  const [changesComment, setChangesComment] = useState("");
  const [postReviewComment, setPostReviewComment] = useState("");
  const [followUps, setFollowUps] = useState<number[]>([]);

  // handleApprove/handleRequestChanges are a human's two answers to a
  // ticket in "review" -- both status writes, merged like Ready's.
  function handleApprove() {
    void store.statusButton(approve, () => apiClient.approveTicket(ticketId, attempt), "Failed to approve the ticket.");
  }

  function handleRequestChanges(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (store.state.status !== "loaded") return;
    void requestChanges.run(async () => {
      const generation = store.beginFieldWrite("status");
      const updated = await apiClient.requestTicketChanges(ticketId, changesComment.trim(), attempt);
      setChangesComment("");
      store.applyStatusWrite(updated, generation);
      await store.refreshComments();
    }, "Failed to request changes.");
  }

  // handleMarkReviewed records the human's look at a merged change. Not a
  // status write, so it reads the status generation rather than bumping it,
  // as posting a comment does: a status the human sets while this is in
  // flight is newer than anything this reply can say -- leaving done clears
  // the mark -- so its reply is the one to believe about the mark.
  function handleMarkReviewed(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (store.state.status !== "loaded") return;
    // The comment stays in the box on failure: it is the only copy of what
    // the human found.
    void postReview.run(async () => {
      const statusGeneration = store.generationOf("status");
      const comment = postReviewComment.trim();
      const updated = await apiClient.markTicketPostReviewed(ticketId, comment);
      setPostReviewComment("");
      if (store.generationOf("status") === statusGeneration) {
        store.updateLoaded((prev) => ({
          ...prev,
          ticket: {
            ...prev.ticket,
            post_reviewed_at: updated.post_reviewed_at,
            post_reviewed_by_user_id: updated.post_reviewed_by_user_id,
            post_reviewed_by_user_id_deleted: updated.post_reviewed_by_user_id_deleted,
            ...timestampsFrom(prev.ticket, updated, false),
          },
        }));
      }
      if (comment) await store.refreshComments();
    }, "Failed to mark the ticket reviewed.");
  }

  return {
    approve,
    requestChanges,
    postReview,
    changesComment,
    setChangesComment,
    postReviewComment,
    setPostReviewComment,
    followUps,
    addFollowUp: (id) => setFollowUps((prev) => [...prev, id]),
    handleApprove,
    handleRequestChanges,
    handleMarkReviewed,
  };
}
