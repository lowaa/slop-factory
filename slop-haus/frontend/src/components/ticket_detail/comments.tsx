import type { Comment, Ticket } from "@/api";
import { AttachmentField } from "@/components/attachments";
import { AuthorName } from "@/components/author_name";
import RichText from "@/shared/rich_text/rich_text";
import Timestamp from "@/shared/timestamp/timestamp";
import type { CommentActions } from "./use_comment_form";
import "./comments.scss";

interface CommentsProps {
  ticket: Ticket;
  comments: Comment[];
  form: CommentActions;
  // Who the box's comment reaches, when that is not the whole ticket's
  // conversation (the page of one attempt of several).
  audience?: string;
  // The attempt the box's comment goes to: the buttons name it. While it is
  // paused for an answer the comment is that answer.
  attempt?: number;
  answering?: boolean;
  // The ticket's own box while its attempts are split: the comment reaches
  // all of the live ones.
  attemptsSplit?: boolean;
}

function postLabel({ attempt, answering, attemptsSplit }: Pick<CommentsProps, "attempt" | "answering" | "attemptsSplit">) {
  if (attempt !== undefined) return answering ? `Answer attempt ${attempt}` : `Comment on attempt ${attempt}`;
  if (attemptsSplit) return "Comment on the ticket (reaches every live attempt)";
  return "Post comment";
}

export function CommentList({ comments }: { comments: Comment[] }) {
  return (
    <>
      <ul>
        {comments.map((c) => (
          <li key={c.id}>
            <div className="ticket-detail__comment-meta">
              <AuthorName agentId={c.author_agent_id} userId={c.author_user_id} userDeleted={c.author_user_id_deleted} />
              <Timestamp at={c.created_at} />
            </div>
            <RichText className="ticket-detail__comment-body" text={c.body} />
          </li>
        ))}
      </ul>
      {comments.length === 0 && <p className="ticket-detail__empty">No comments yet.</p>}
    </>
  );
}

export default function Comments({ ticket, comments, form, audience, attempt, answering, attemptsSplit }: CommentsProps) {
  const { submit } = form;
  const post = postLabel({ attempt, answering, attemptsSplit });
  const comment = attempt === undefined ? "Comment" : `Comment on attempt ${attempt}`;
  const flagLabel = `${comment} & ${ticket.needs_discussion ? "clear " : ""}needs discussion`;
  return (
    <section className="ticket-detail__comments">
      <h2>Comments</h2>
      <CommentList comments={comments} />
      {audience && <p className="ticket-detail__hint">{audience}</p>}

      <form onSubmit={form.handleAddComment}>
        <AttachmentField uploader={form.uploader} disabled={submit.busy}>
          <label>
            <span>Add a comment</span>
            <textarea rows={3} required value={form.body} onChange={(e) => form.setBody(e.target.value)} />
          </label>
        </AttachmentField>
        {submit.error && (
          <p role="alert" className="ticket-detail__notice">
            {submit.error}
          </p>
        )}
        <div className="ticket-detail__comment-actions">
          <button type="submit" disabled={submit.busy}>
            {submit.busy ? "Posting…" : post}
          </button>
          {/* Not a submit button, so the box's required check is
              ours to make: disabled while there is nothing to post. */}
          <button
            type="button"
            disabled={submit.busy || !form.body.trim()}
            onClick={() => form.handleCommentSettingNeedsDiscussion(!ticket.needs_discussion)}
          >
            {flagLabel}
          </button>
        </div>
      </form>
    </section>
  );
}
