import { useState, type FormEvent } from "react";
import { apiClient, ApiError, type Attachment } from "@/api";
import { useAttachmentUploader, type AttachmentUploader } from "@/components/attachments";
import { useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface CommentActions {
  body: string;
  setBody: (body: string) => void;
  submit: AsyncAction;
  // The images and videos collected for the comment: uploaded to the ticket
  // when it is posted, before it.
  uploader: AttachmentUploader;
  handleAddComment: (e: FormEvent<HTMLFormElement>) => void;
  handleCommentSettingNeedsDiscussion: (needsDiscussion: boolean) => void;
}

// useCommentForm is the box for adding a comment. Its state is held by
// TicketDetail, which calls this, for the reason useReviewActions gives.
//
// attempt addresses what is posted to one attempt of a ticket that has
// several, as the page of that attempt does; left out, a comment is the
// ticket's own and reaches every live attempt.
//
// onAttached hears of the attachments a posted comment brought with it.
export function useCommentForm(
  store: TicketStore,
  ticketId: number,
  attempt?: number,
  onAttached?: (attachments: Attachment[]) => void,
): CommentActions {
  const [body, setBody] = useState("");
  const uploader = useAttachmentUploader();
  // Both comment buttons: one request at a time, and a failure reported
  // beside the box, which keeps what was typed.
  const submit = useAsyncAction();

  function submitComment(post: () => Promise<void>) {
    if (store.state.status !== "loaded") return;
    void submit.run(post, "Failed to add comment.");
  }

  function handleAddComment(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    submitComment(postComment);
  }

  // The files go first: a comment that promised a screenshot is not posted
  // without it. A failure leaves the done ones marked, so posting again sends
  // only what is left.
  async function uploadAttachments() {
    const sent = await uploader.uploadAll((file, { onProgress }) =>
      apiClient.uploadTicketAttachment(ticketId, file, { onProgress }),
    );
    if (!sent) throw new ApiError(0, "Some attachments did not upload. Remove them or try again.");
  }

  function attachmentsPosted() {
    const posted = uploader.uploaded();
    uploader.clear();
    if (posted.length > 0) onAttached?.(posted);
  }

  async function postComment() {
    await uploadAttachments();
    // Read, not bumped: this is not a status write of its own, but a
    // status write the human makes while it is in flight (a PATCH or Ready)
    // is newer than anything the refresh below can report.
    const statusGeneration = store.generationOf("status");
    const comment = await apiClient.addComment(ticketId, body.trim(), attempt);
    setBody("");
    attachmentsPosted();
    store.updateLoaded((prev) => ({ ...prev, comments: [...prev.comments, comment] }));

    // A comment posted here with no author is the human's, and on a
    // ticket in "discussion" the server treats it as the answer and
    // moves the ticket back to open/blocked as a side effect of this
    // same request -- a refetch is the only way the UI finds out.
    // Best-effort: the comment above already landed, so a failed
    // refresh here just leaves the ticket showing its pre-answer state
    // until the next successful load.
    try {
      const [ticket, contexts] = await Promise.all([
        apiClient.getTicket(ticketId),
        apiClient.listAgentContexts(ticketId),
      ]);
      // Only what a human comment can change. The rest of the ticket may
      // have been edited while this was in flight -- a priority change
      // landing first -- and a whole-ticket overwrite would put the older
      // value back. The same goes for the status itself, and what travels
      // with it, if the human set one meanwhile.
      const statusSuperseded = store.generationOf("status") !== statusGeneration;
      store.updateLoaded((prev) => ({
        ...prev,
        contexts,
        ticket: {
          ...prev.ticket,
          ...(statusSuperseded ? {} : { status: ticket.status }),
          ...timestampsFrom(prev.ticket, ticket, !statusSuperseded),
        },
      }));
    } catch {
      // See above -- swallowed deliberately.
    }
  }

  // The comment and the flag in one request, so no agent can claim the
  // ticket between them. What it moved -- an answered discussion
  // requeued, a ticket taken from the agent working it -- comes back in
  // the response and is shown from it. It writes the flag and the status,
  // so it takes a turn on both, like a PATCH of them: a change the human
  // makes to either while this is in flight is the newer one.
  async function postCommentSettingNeedsDiscussion(needsDiscussion: boolean) {
    await uploadAttachments();
    const flagGeneration = store.beginFieldWrite("needs_discussion");
    const statusGeneration = store.beginFieldWrite("status");
    const { comment, ticket } = await apiClient.addCommentSettingNeedsDiscussion(
      ticketId,
      body.trim(),
      needsDiscussion,
      attempt,
    );
    setBody("");
    attachmentsPosted();
    const flagCurrent = store.isCurrent("needs_discussion", flagGeneration);
    const statusCurrent = store.isCurrent("status", statusGeneration);
    store.updateLoaded((prev) => ({
      ...prev,
      comments: [...prev.comments, comment],
      ticket: {
        ...prev.ticket,
        ...(flagCurrent ? { needs_discussion: ticket.needs_discussion } : {}),
        ...(statusCurrent ? { status: ticket.status } : {}),
        ...timestampsFrom(prev.ticket, ticket, statusCurrent),
      },
    }));
  }

  return {
    body,
    setBody,
    submit,
    uploader,
    handleAddComment,
    handleCommentSettingNeedsDiscussion: (needsDiscussion) =>
      submitComment(() => postCommentSettingNeedsDiscussion(needsDiscussion)),
  };
}
