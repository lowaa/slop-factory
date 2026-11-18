import { useId } from "react";
import type { Ticket } from "@/api";
import { Button } from "@slop-factory/ui";
import { holdsScope, type PathsEditorActions } from "./use_paths_editor";
import "./paths_editor.scss";

interface PathsEditorProps {
  ticket: Ticket;
  editor: PathsEditorActions;
}

export default function PathsEditor({ ticket, editor }: PathsEditorProps) {
  const { pathsEdit } = editor;
  const textId = useId();
  const extended = ticket.paths.filter((p) => p.source === "extended");
  return (
    <section className="ticket-detail__paths-section">
      <h2>Path scopes</h2>
      {ticket.paths.length > 0 ? (
        <ul className="ticket-detail__paths">
          {ticket.paths.map((p) => (
            <li key={p.path}>
              {p.path} <span className="ticket-detail__path-source">({p.source})</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="ticket-detail__empty">No path scopes.</p>
      )}

      {pathsEdit ? (
        <form
          className="ticket-detail__paths-form"
          onSubmit={(e) => {
            e.preventDefault();
            editor.handleSave();
          }}
        >
          <label htmlFor={textId}>Path scopes (one per line)</label>
          <textarea
            id={textId}
            rows={4}
            autoFocus
            placeholder={"server/internal/ticket/\nfrontend/src/components/board/"}
            value={pathsEdit.text}
            disabled={pathsEdit.saving}
            onChange={(e) => editor.setText(e.target.value)}
          />
          <p className="ticket-detail__hint">
            Saving replaces the filed paths. An empty list clears them.
            {extended.length > 0 && " Paths an agent extended are kept as they are."}
          </p>
          {holdsScope(ticket) && (
            <p role="status" className="ticket-detail__paths-held">
              An agent holds this ticket. The new scope applies to its claim immediately, and a comment
              recording the change is added so it can read it.
            </p>
          )}
          {pathsEdit.error && (
            <p role="alert" className="ticket-detail__notice">
              {pathsEdit.error}
            </p>
          )}
          <div className="ticket-detail__paths-actions">
            <Button type="submit" variant="primary" disabled={pathsEdit.saving}>
              {pathsEdit.saving ? "Saving…" : "Save"}
            </Button>
            <Button onClick={editor.cancel} disabled={pathsEdit.saving}>
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <Button size="sm" className="ticket-detail__edit-paths" onClick={editor.startEditing}>
          Edit path scopes
        </Button>
      )}
    </section>
  );
}
