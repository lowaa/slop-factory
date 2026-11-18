import type { Ticket } from "@/api";
import RichText from "@/shared/rich_text/rich_text";
import type { BodyEditorActions } from "./use_body_editor";
import AttemptEditWarning from "./attempt_edit_warning_note";
import "./body_editor.scss";

interface BodyEditorProps {
  ticket: Ticket;
  editor: BodyEditorActions;
}

export default function BodyEditor({ ticket, editor }: BodyEditorProps) {
  const { edit, setEdit, save } = editor;
  return (
    <section className="ticket-detail__body-section">
      {edit ? (
        <form className="ticket-detail__body-form" onSubmit={editor.handleSaveEdit}>
          <label>
            <span>Title</span>
            <input
              type="text"
              autoFocus
              value={edit.title}
              onChange={(e) => setEdit({ ...edit, title: e.target.value })}
            />
          </label>
          <label>
            <span>Description</span>
            <textarea rows={12} value={edit.body} onChange={(e) => setEdit({ ...edit, body: e.target.value })} />
          </label>
          {editor.warning && <AttemptEditWarning message={editor.warning} />}
          {save.error && (
            <p role="alert" className="ticket-detail__notice">
              {save.error}
            </p>
          )}
          <div className="ticket-detail__body-actions">
            <button type="submit" disabled={save.busy}>
              {save.busy ? "Saving…" : editor.warning ? "Save anyway" : "Save"}
            </button>
            <button type="button" onClick={() => setEdit(null)} disabled={save.busy}>
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <>
          {ticket.body ? (
            <RichText className="ticket-detail__body" text={ticket.body} />
          ) : (
            <p className="ticket-detail__empty">No description yet.</p>
          )}
          <button type="button" className="ticket-detail__edit-body" onClick={editor.startEditing}>
            Edit title and description
          </button>
        </>
      )}
    </section>
  );
}
