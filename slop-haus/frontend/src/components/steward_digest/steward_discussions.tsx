import type { StewardDiscussions, StewardDiscussionScore } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";

function percent(rate: number | null): string {
  return rate === null ? "No decisions" : `${Math.round(rate * 100)}%`;
}

function warning(row: StewardDiscussionScore): string | null {
  const reasons = [
    row.warn_few_decisions && "too few decisions to rely on",
    row.warn_low_agreement && "agreement below your threshold",
  ];
  const text = reasons.filter(Boolean).join("; ");
  return text === "" ? null : text;
}

function Row({ row, label }: { row: StewardDiscussionScore; label: string }) {
  const warn = warning(row);
  return (
    <tr className={warn === null ? undefined : "steward-digest__row--warn"}>
      <th scope="row">{label}</th>
      <td>{row.decisions}</td>
      <td title={`${row.agreed} agreed, ${row.disagreed} disagreed`}>
        {percent(row.agreement_rate)}
      </td>
      <td>{warn === null ? "" : `Warning: ${warn}`}</td>
    </tr>
  );
}

function ScoreTable({
  title,
  rows,
  total,
}: {
  title: string;
  rows: StewardDiscussionScore[];
  total: StewardDiscussionScore;
}) {
  return (
    <table className="steward-digest__table" aria-label={title}>
      <thead>
        <tr>
          <th scope="col">{title}</th>
          <th scope="col">Decided</th>
          <th scope="col">Agreement</th>
          <th scope="col">Warnings</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <Row key={r.category} row={r} label={r.category} />
        ))}
        <Row row={total} label="All" />
      </tbody>
    </table>
  );
}

function Ticket({ id, round }: { id: number; round: number }) {
  return (
    <>
      <TicketLink ticketId={id} from="steward">
        #{id}
      </TicketLink>{" "}
      round {round}
    </>
  );
}

// StewardDiscussionsSection shows how the owner's answers to discussions
// compared with the steward's label and its drafted answer: the numbers a
// choice of autonomy level depends on. The quoted bodies are the owner's and
// the steward's text, shown as plain text.
export default function StewardDiscussionsSection({
  discussions: d,
}: {
  discussions: StewardDiscussions;
}) {
  const decided = d.labels_total.decisions + d.answers_total.decisions > 0;
  return (
    <section className="steward-digest__discussions" aria-label="Discussions">
      <h2>Discussions</h2>
      {!decided ? (
        <p className="steward-digest__note">
          No decided discussions in this period.
        </p>
      ) : (
        <>
          <ScoreTable title="Label" rows={d.labels} total={d.labels_total} />
          <ScoreTable title="Answer" rows={d.answers} total={d.answers_total} />
          <p className="steward-digest__note">
            Label agreement is how often you answered as the steward's label
            expected: a routine round with its template, a yours round with your
            own words. Answer agreement is how often you approved the drafted
            answer rather than editing or rejecting it.
          </p>
          {d.label_disagreements.length > 0 && (
            <ul
              className="steward-digest__decisions"
              aria-label="Label disagreements"
            >
              {d.label_disagreements.map((x) => (
                <li key={`${x.ticket_id}-${x.attempt}-${x.round}`}>
                  <Ticket id={x.ticket_id} round={x.round} /> {x.category}:
                  labelled {x.label}, you {x.how}
                  {x.overridden && " (routine, answered another way)"} (
                  {new Date(x.at).toLocaleString()})
                </li>
              ))}
            </ul>
          )}
          {d.answer_disagreements.length > 0 && (
            <ul
              className="steward-digest__decisions"
              aria-label="Answer disagreements"
            >
              {d.answer_disagreements.map((x) => (
                <li key={`${x.ticket_id}-${x.attempt}-${x.round}`}>
                  <Ticket id={x.ticket_id} round={x.round} /> {x.category}: you{" "}
                  {x.how === "edit" ? "edited" : "rejected"} the draft (
                  {new Date(x.at).toLocaleString()})
                  {x.reason !== "" && <> — {x.reason}</>}
                  <blockquote className="steward-digest__quote">
                    Draft: {x.original_body}
                  </blockquote>
                  {x.how === "edit" && (
                    <blockquote className="steward-digest__quote">
                      Posted: {x.final_body}
                    </blockquote>
                  )}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}
