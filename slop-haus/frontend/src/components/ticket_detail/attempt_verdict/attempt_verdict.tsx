import { Button } from "@slop-factory/ui";
import type { AttemptVerdict as Verdict, AttemptVerdictValue, TicketAttempt } from "@/api";
import MemberName from "@/shared/names/member_name";
import type { TicketStore } from "../use_ticket_store";
import { judgeable } from "./judgeable";
import { useAttemptVerdict } from "./use_attempt_verdict";
import "./attempt_verdict.scss";

const CHOICES: { value: AttemptVerdictValue; label: string }[] = [
  { value: "would_merge", label: "Would merge" },
  { value: "would_merge_with_changes", label: "Would merge with changes" },
  { value: "would_not_merge", label: "Would not merge" },
];

const NOTE_LIMIT = 1000;

const labelOf = (v: AttemptVerdictValue) => CHOICES.find((c) => c.value === v)?.label ?? v;

function When({ at }: { at: string }) {
  return <time dateTime={at}>{new Date(at).toLocaleString()}</time>;
}

function Entry({ entry }: { entry: Pick<Verdict, "verdict" | "note" | "set_at" | "set_by_user_id" | "set_by_user_id_deleted"> }) {
  return (
    <>
      <strong>{labelOf(entry.verdict)}</strong> by{" "}
      <MemberName id={entry.set_by_user_id} deleted={entry.set_by_user_id_deleted} /> <When at={entry.set_at} />
      {entry.note && <q className="attempt-verdict__note">{entry.note}</q>}
    </>
  );
}

// A person's judgement of a finished attempt, kept apart from its signals:
// the two are shown side by side and never combined. Unfinished attempts
// show nothing, and so does an attempt's tab for a worker (the server sends
// no verdict to one, and it could not set one).
export default function AttemptVerdict({
  store,
  ticketId,
  attempt,
}: {
  store: TicketStore;
  ticketId: number;
  attempt: TicketAttempt;
}) {
  const form = useAttemptVerdict(store, ticketId, attempt.number);
  if (!judgeable(attempt)) return null;
  const { verdict } = attempt;
  return (
    <section className="attempt-verdict" aria-label="Your verdict">
      <h2>Verdict</h2>
      {verdict ? (
        <p className="attempt-verdict__current">
          <Entry entry={verdict} />
        </p>
      ) : (
        <p>No verdict yet.</p>
      )}
      {form.submit.error && (
        <p role="alert" className="ticket-detail__notice">
          {form.submit.error}
        </p>
      )}
      <label className="attempt-verdict__note-field">
        Note (optional)
        <textarea
          value={form.note}
          maxLength={NOTE_LIMIT}
          rows={2}
          onChange={(e) => form.setNote(e.target.value)}
        />
      </label>
      <div className="attempt-verdict__choices" role="group" aria-label="Set your verdict">
        {CHOICES.map((c) => (
          <Button
            key={c.value}
            variant={verdict?.verdict === c.value ? "primary" : "secondary"}
            disabled={form.submit.busy}
            onClick={() => form.choose(c.value)}
          >
            {c.label}
          </Button>
        ))}
      </div>
      {verdict && verdict.history.length > 0 && (
        <details className="attempt-verdict__history">
          <summary>Earlier verdicts ({verdict.history.length})</summary>
          <ul>
            {verdict.history.map((h) => (
              <li key={h.set_at}>
                <Entry entry={h} />
              </li>
            ))}
          </ul>
        </details>
      )}
    </section>
  );
}
