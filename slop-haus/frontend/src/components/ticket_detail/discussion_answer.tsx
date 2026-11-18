import { Button } from "@slop-factory/ui";
import type { DiscussionLabel, StewardApply, TicketAttempt } from "@/api";
import DiscussShortcut from "./discuss_shortcut";
import { useDiscussionAnswer, type DiscussionAnswer as Answer } from "./use_discussion_answer";
import "./discussion_answer.scss";

type LabelKind = NonNullable<NonNullable<TicketAttempt["discussion"]>["label"]>;

function badgeText(label: LabelKind, category: string | undefined): string {
  switch (label) {
    case "routine":
      return `Routine: ${category ?? ""}`;
    case "yours":
      return "Yours: judgment";
    case "not_assessed":
      return "Not assessed";
    case "pending":
      return "Assessing…";
  }
}

// A round the steward has not labelled at all (level 0, or before the steward's
// labelling reaches it) still gets the widget: Assess now is the way in.
function badgeFor(discussion: TicketAttempt["discussion"]): { label: LabelKind | "none"; text: string } {
  return discussion?.label
    ? { label: discussion.label, text: badgeText(discussion.label, discussion.category) }
    : { label: "none", text: "Not labelled yet" };
}

// Discussion answers are never undoable: a worker may already have read one.
function applyText(a: StewardApply): string {
  const at = a.at ? new Date(a.at).toLocaleString() : "";
  switch (a.state) {
    case "applied":
      return "Applied by steward";
    case "scheduled":
      return `Applies at ${at} unless you veto`;
    case "vetoed":
      return "You vetoed the steward's answer";
  }
}

function Evidence({ label }: { label: DiscussionLabel }) {
  if (label.evidence.length === 0) return null;
  return (
    <ul className="ticket-detail__answer-evidence" aria-label="Checks">
      {label.evidence.map((e) => (
        <li key={`${e.check}/${e.ref}`} data-ok={e.ok}>
          <strong>{e.ok ? "Pass" : "Fail"}</strong> {e.check} <code>{e.ref}</code>
          {e.detail && <> — {e.detail}</>}
        </li>
      ))}
    </ul>
  );
}

function UnlabelledPanel({ answer }: { answer: Answer }) {
  return (
    <div className="ticket-detail__answer-draft">
      <p>The steward has not labelled this round.</p>
      <div className="ticket-detail__answer-buttons">
        <Button onClick={answer.assess} disabled={answer.action.busy}>
          Assess now
        </Button>
      </div>
    </div>
  );
}

function DraftPanel({ answer }: { answer: Answer }) {
  const { draft, mode, input, action } = answer;
  const label = draft?.label;
  if (!answer.labelled && !label) return <UnlabelledPanel answer={answer} />;
  if (!draft || !label) return action.error ? null : <p>Loading…</p>;
  const routine = label.label === "routine";
  // The server only takes a decision on a round it has classified.
  const decidable = routine || label.label === "yours";
  return (
    <div className="ticket-detail__answer-draft">
      {answer.stale && (
        <p role="alert" className="ticket-detail__notice">
          {answer.stale} The draft below is refreshed.
        </p>
      )}
      <p>{label.reason}</p>
      {draft.steward_apply && <p role="status">{applyText(draft.steward_apply)}</p>}
      {routine && (
        <>
          <p>
            Category <strong>{label.category}</strong>
          </p>
          <blockquote className="ticket-detail__answer-body">{draft.body}</blockquote>
        </>
      )}
      <Evidence label={label} />
      {draft.decision ? (
        <p role="status">This round was decided: {draft.decision.action}.</p>
      ) : mode === "view" ? (
        <div className="ticket-detail__answer-buttons">
          {routine && (
            <Button variant="primary" onClick={answer.approve} disabled={action.busy}>
              Approve
            </Button>
          )}
          {decidable && (
            <>
              <Button onClick={() => answer.setMode("edit")} disabled={action.busy}>
                Edit
              </Button>
              <Button onClick={() => answer.setMode("reject")} disabled={action.busy}>
                Reject
              </Button>
            </>
          )}
          {draft.steward_apply?.can_veto && (
            <Button variant="danger" onClick={answer.veto} disabled={action.busy}>
              Veto
            </Button>
          )}
          {label.label === "not_assessed" && (
            <Button onClick={answer.assess} disabled={action.busy}>
              Assess now
            </Button>
          )}
        </div>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            answer.submit();
          }}
        >
          <label>
            {mode === "edit" ? "Your answer" : "Reason"}
            <textarea value={input} onChange={(e) => answer.setInput(e.target.value)} rows={4} />
          </label>
          <div className="ticket-detail__answer-buttons">
            <Button type="submit" disabled={action.busy || input.trim() === ""}>
              {mode === "edit" ? "Post my answer" : "Reject draft"}
            </Button>
            <Button onClick={() => answer.setMode("view")} disabled={action.busy}>
              Cancel
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}

// DiscussionAnswer is the one place to answer an attempt's open discussion:
// the steward's label and draft, and the shortcut into Remote Control. It is
// never hidden: a round without a label row reads "Not labelled yet" and
// offers Assess now.
export default function DiscussionAnswer({ ticketId, attempt }: { ticketId: number; attempt: TicketAttempt }) {
  const answer = useDiscussionAnswer(ticketId, attempt);
  const discussion = attempt.discussion;
  const badge = badgeFor(discussion);
  return (
    <section className="ticket-detail__answer" aria-label={`Answer this discussion, attempt ${attempt.number}`}>
      <strong className="ticket-detail__answer-title">Answer this discussion</strong>
      <p>
        Attempt {attempt.number}:{" "}
        <span className="ticket-detail__answer-badge" data-label={badge.label}>
          {badge.text}
        </span>{" "}
        {discussion?.reason}
      </p>
      <div className="ticket-detail__answer-buttons">
        <Button variant="tonal" onClick={answer.toggle}>
          Answer with steward
        </Button>
        <DiscussShortcut ticketId={ticketId} />
      </div>
      {answer.open && <DraftPanel answer={answer} />}
      {answer.action.error && (
        <p role="alert" className="ticket-detail__notice">
          {answer.action.error}
        </p>
      )}
    </section>
  );
}
