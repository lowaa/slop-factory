import { useState, type FormEvent } from "react";
import { apiClient, ApiError, type Ticket } from "@/api";
import { useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { editWarning } from "./attempt_edit_warning";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

// EditState is the title-and-description editor while it is open. base is
// the ticket's revision when it was opened, sent with the save so an edit
// someone else made meanwhile is refused rather than silently overwritten.
interface EditState {
  title: string;
  body: string;
  base: number;
}

export interface BodyEditorActions {
  edit: EditState | null;
  setEdit: (edit: EditState | null) => void;
  // warning is set once a save was held back for the attempts running under
  // the ticket; saving again confirms it.
  warning: string | null;
  save: AsyncAction;
  startEditing: () => void;
  handleSaveEdit: (e: FormEvent<HTMLFormElement>) => void;
}

// useBodyEditor is the title-and-description editor. Its state is held by
// TicketDetail, which calls this, for the reason useReviewActions gives.
export function useBodyEditor(store: TicketStore, ticketId: number): BodyEditorActions {
  const [edit, setEditState] = useState<EditState | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const save = useAsyncAction();

  // Any change to the draft, or closing it, withdraws a pending confirmation:
  // the person confirmed the text they saw.
  function setEdit(next: EditState | null) {
    setWarning(null);
    setEditState(next);
  }

  function startEditing() {
    if (store.state.status !== "loaded") return;
    const { ticket } = store.state;
    setEdit({ title: ticket.title, body: ticket.body, base: ticket.revision });
    save.setError(null);
  }

  // Only the fields the human actually changed are sent, so the revision
  // records what they meant to change -- and saving with nothing changed
  // just closes the editor.
  function handleSaveEdit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (store.state.status !== "loaded" || !edit) return;
    const { ticket } = store.state;
    if (edit.title.trim() === "") {
      save.setError("The title can't be empty.");
      return;
    }
    const input = {
      ...(edit.title !== ticket.title && { title: edit.title }),
      ...(edit.body !== ticket.body && { body: edit.body }),
    };
    if (Object.keys(input).length === 0) {
      setEdit(null);
      return;
    }
    const attemptsWarning = editWarning(ticket.attempts);
    if (attemptsWarning && warning === null) {
      setWarning(attemptsWarning);
      return;
    }
    // On any failure the editor and the draft stay: what the human just
    // typed is the only copy of it, and swapping the view for an error
    // screen would throw it away to report that it could not be saved.
    void save.run(async () => {
      let updated: Ticket;
      try {
        updated = await apiClient.editTicket(ticketId, { ...input, base_revision: edit.base });
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 409)) throw err;
        save.setError(
          "Someone else edited this ticket after you started, and their change is in the history below. Copy anything you want to keep, then cancel and edit again.",
        );
        await refreshAfterConflict();
        return;
      }
      setEdit(null);
      store.updateLoaded((prev) => ({
        ...prev,
        ticket: {
          ...prev.ticket,
          title: updated.title,
          body: updated.body,
          revision: updated.revision,
          ...timestampsFrom(prev.ticket, updated, false),
        },
      }));
      // Best-effort, like the post-comment refresh: the edit has landed, and
      // a failed refetch only leaves the history a revision behind until the
      // next load.
      try {
        const revisions = await apiClient.listRevisions(ticketId);
        store.updateLoaded((prev) => ({ ...prev, revisions }));
      } catch {
        // See above -- swallowed deliberately.
      }
    }, "Failed to save the edit.");
  }

  // Without this, cancel-and-edit-again after a refused save would reopen
  // on the same stale text and revision, and be refused the same way
  // forever. The open editor keeps its stale base: saving over a change the
  // human has not looked at yet is exactly what the 409 exists to prevent.
  async function refreshAfterConflict() {
    try {
      const [ticket, revisions] = await Promise.all([apiClient.getTicket(ticketId), apiClient.listRevisions(ticketId)]);
      store.updateLoaded((prev) => ({
        ...prev,
        revisions,
        ticket: {
          ...prev.ticket,
          title: ticket.title,
          body: ticket.body,
          revision: ticket.revision,
          ...timestampsFrom(prev.ticket, ticket, false),
        },
      }));
    } catch {
      // Best-effort: the next load shows it.
    }
  }

  return { edit, setEdit, warning, save, startEditing, handleSaveEdit };
}
