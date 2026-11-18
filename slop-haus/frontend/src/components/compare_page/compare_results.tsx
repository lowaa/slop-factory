import type { CompareMeasure, ComparePair, CompareResult, CompareVerdicts } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";
import {
  formatValue,
  groupMeasures,
  notCounted,
  pairRows,
  runsText,
  smallNCaveat,
  spreadText,
  variantLabel,
} from "./compare_model";
import "./compare_page.scss";

function VerdictCounts({ label, v }: { label: string; v: CompareVerdicts }) {
  return (
    <li>
      {label}: {v.would_merge} would merge, {v.would_merge_with_changes} with changes, {v.would_not_merge} would not,{" "}
      {v.unjudged} unjudged
    </li>
  );
}

function MeasureTable({ pair, label, measures }: { pair: ComparePair; label: string; measures: CompareMeasure[] }) {
  return (
    <table className="compare-page__table">
      <caption>{label}</caption>
      <thead>
        <tr>
          <th scope="col">Measure</th>
          <th scope="col">Median difference</th>
          <th scope="col">Spread</th>
          <th scope="col">n</th>
        </tr>
      </thead>
      <tbody>
        {measures.map((m) => {
          const s = pair.measures[m.key];
          return (
            <tr key={m.key}>
              <th scope="row" title={m.doc}>
                {m.key}
              </th>
              {s ? (
                <>
                  <td>{formatValue(s.median, m.unit, true)}</td>
                  <td>{spreadText(s, m.unit)}</td>
                  <td>{s.n}</td>
                </>
              ) : (
                <td colSpan={3}>unknown: no ticket has it on both sides</td>
              )}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function PairResult({ result, pair }: { result: CompareResult; pair: ComparePair }) {
  const rows = pairRows(result, pair);
  const caveat = smallNCaveat(pair.tickets);
  const name = variantLabel(pair.challenger);
  return (
    <section className="compare-page__pair" aria-label={`${name} versus ${variantLabel(result.baseline)}`}>
      <h3>
        {name} versus {variantLabel(result.baseline)}
      </h3>
      <p>
        {pair.tickets} {pair.tickets === 1 ? "ticket" : "tickets"} in both. Runs per ticket: baseline{" "}
        {runsText(rows, "baseline")}, challenger {runsText(rows, "challenger_side")}.
      </p>
      {caveat && (
        <p role="note" className="compare-page__caveat" data-testid="small-n">
          {caveat}
        </p>
      )}
      {pair.flagged_tickets > 0 && (
        <p role="note" className="compare-page__caveat" data-testid="run-with-flagged">
          {pair.flagged_tickets} of these tickets ran with more than the variant differing (a model, harness or
          instructions change); those rows are a less clean comparison.
        </p>
      )}
      {groupMeasures(result.measures).map((g) => (
        <MeasureTable key={g.group} pair={pair} label={g.label} measures={g.measures} />
      ))}
      <h4>Verdicts</h4>
      <ul>
        <VerdictCounts label="Baseline" v={pair.verdicts.baseline} />
        <VerdictCounts label="Challenger" v={pair.verdicts.challenger} />
      </ul>
      <details>
        <summary>Per ticket ({rows.length})</summary>
        <table className="compare-page__table">
          <thead>
            <tr>
              <th scope="col">Ticket</th>
              <th scope="col">Attempts</th>
              <th scope="col">Flags</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.ticket_id} className={r.run_with_flags.length > 0 ? "is-flagged" : undefined}>
                <th scope="row">
                  <TicketLink ticketId={r.ticket_id} from="compare">
                    #{r.ticket_id}
                  </TicketLink>
                </th>
                <td>
                  {r.baseline.attempts.map((n) => (
                    <TicketLink key={`b${n}`} ticketId={r.ticket_id} from="compare" attempt={n}>
                      baseline attempt {n}
                    </TicketLink>
                  ))}{" "}
                  {r.challenger_side.attempts.map((n) => (
                    <TicketLink key={`c${n}`} ticketId={r.ticket_id} from="compare" attempt={n}>
                      challenger attempt {n}
                    </TicketLink>
                  ))}
                </td>
                <td>{r.run_with_flags.length > 0 ? `differs in ${r.run_with_flags.join(", ")}` : ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </section>
  );
}

// The results are the server's figures laid out per challenger and group.
// Nothing here combines groups into a score.
export default function CompareResults({ result }: { result: CompareResult }) {
  const skipped = notCounted(result.measures);
  return (
    <div className="compare-page__results">
      <p>
        {result.tickets_in_set} {result.tickets_in_set === 1 ? "ticket" : "tickets"} in the set
        {result.size_filtered > 0 && `, ${result.size_filtered} left out by size`}
        {result.unfinished_attempts > 0 && `, ${result.unfinished_attempts} unfinished attempts not counted`}.
      </p>
      {result.pairs.map((pair) => (
        <PairResult key={pair.challenger} result={result} pair={pair} />
      ))}
      {(result.notes.length > 0 || skipped.length > 0) && (
        <ul className="compare-page__notes" aria-label="Not counted">
          {result.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
          {skipped.map((m) => (
            <li key={m.key}>
              {m.key}: {m.doc}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
