import { useState } from "react";
import { apiClient, type Ticket } from "@/api";
import { splitPaths } from "@/lib/split_paths";
import { errorMessage } from "@/lib/use_async_action";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

// Only filed paths are the person's to edit: one an agent extended is its
// grant for work under way, which the server's edit leaves in place.
function filedPaths(ticket: Ticket): string[] {
  return ticket.paths.filter((p) => p.source !== "extended").map((p) => p.path);
}

// An agent holds the ticket's scope while it works or while its change is in
// review, so an edit there lands on a claim that is already in use.
export function holdsScope(ticket: Ticket): boolean {
  return ticket.status === "in_progress" || ticket.status === "review";
}

interface PathsEditState {
  text: string;
  saving: boolean;
  error: string | null;
}

export interface PathsEditorActions {
  pathsEdit: PathsEditState | null;
  setText: (text: string) => void;
  startEditing: () => void;
  cancel: () => void;
  handleSave: () => void;
}

// usePathsEditor is the path scope editor. Its state is held by
// TicketDetail, which calls this, for the reason useReviewActions gives.
export function usePathsEditor(store: TicketStore, ticketId: number): PathsEditorActions {
  const [pathsEdit, setPathsEdit] = useState<PathsEditState | null>(null);

  function startEditing() {
    if (store.state.status !== "loaded") return;
    setPathsEdit({
      text: filedPaths(store.state.ticket).join("\n"),
      saving: false,
      error: null,
    });
  }

  // No write generations, unlike the roles editor: the form is locked while
  // a save is in flight and nothing else writes paths, so there is never a
  // second reply to order against this one.
  async function save() {
    if (store.state.status !== "loaded" || !pathsEdit) return;
    const paths = splitPaths(pathsEdit.text);
    setPathsEdit({ ...pathsEdit, saving: true, error: null });
    let updated: Ticket;
    try {
      updated = await apiClient.updateTicket(ticketId, {
        paths: paths.map((path) => ({ path })),
      });
    } catch (err) {
      setPathsEdit({
        text: pathsEdit.text,
        saving: false,
        error: errorMessage(err, "Failed to update the path scopes."),
      });
      return;
    }
    store.updateLoaded((prev) => ({
      ...prev,
      ticket: {
        ...prev.ticket,
        paths: updated.paths,
        scope_overlaps: updated.scope_overlaps,
        ...timestampsFrom(prev.ticket, updated, false),
      },
    }));
    setPathsEdit(null);

    // The server records a held ticket's scope change as a comment, written
    // with the edit, so reload them to show it here.
    if (holdsScope(updated)) await store.refreshComments();
  }

  return {
    pathsEdit,
    setText: (text) => setPathsEdit((prev) => (prev ? { ...prev, text } : prev)),
    startEditing,
    cancel: () => setPathsEdit(null),
    handleSave: () => void save(),
  };
}
