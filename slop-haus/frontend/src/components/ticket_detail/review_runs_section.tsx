import { useId, useState } from "react";
import type { ReviewFinding, ReviewRun, TicketAttempt } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";
import { describeVerdict, groupByHead, sameHead, shortSha } from "./review_runs_model";
import { useReviewRuns } from "./use_review_runs";
import "./review_runs_section.scss";

function FindingRow({ finding }: { finding: ReviewFinding }) {
  const [open, setOpen] = useState(false);
  const detailId = useId();
  const where = finding.line > 0 ? `${finding.file}:${finding.line}` : finding.file;
  const { label } = finding;
  return (
    <li className="review-runs__finding">
      <button
        type="button"
        className="review-runs__finding-toggle"
        aria-expanded={open}
        aria-controls={detailId}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={`review-runs__severity review-runs__severity--${finding.severity}`}>{finding.severity}</span>
        <code className="review-runs__where">{where}</code>
        <span>{finding.summary}</span>
      </button>
      <div id={detailId} hidden={!open} className="review-runs__finding-body">
        {open && (
          <>
            <p className="review-runs__detail">{finding.detail}</p>
            {label ? (
              <p className="review-runs__label">
                <strong>{describeVerdict(label.verdict)}</strong>
                {label.reason && `: ${label.reason}`}
                {label.fixed_in && (
                  <>
                    {" "}
                    (fixed in <code>{shortSha(label.fixed_in)}</code>)
                  </>
                )}
              </p>
            ) : (
              <p className="review-runs__label review-runs__label--none">unlabelled</p>
            )}
          </>
        )}
      </div>
    </li>
  );
}

function RunRow({ run }: { run: ReviewRun }) {
  const failed = run.status === "failed";
  const who = `${run.posted_by_kind} ${run.posted_by_name}`;
  return (
    <li className="review-runs__run">
      <div className="review-runs__run-head">
        <strong>{run.personas.join(", ") || "no persona"}</strong>
        {failed ? (
          <span className="review-runs__badge review-runs__badge--bad">failed</span>
        ) : (
          <span className="review-runs__badge">{describeVerdict(run.verdict)}</span>
        )}
        {run.incomplete && <span className="review-runs__badge review-runs__badge--warn">incomplete</span>}
        {run.kind === "bench" && <span className="review-runs__badge">bench</span>}
        <span className="review-runs__muted">
          by {who}, {run.provider} {run.model}, <Timestamp at={run.finished_at} />
        </span>
      </div>
      {run.failure && (
        <p role="alert" className="review-runs__failure">
          Failed ({run.failure.class}): {run.failure.message}
        </p>
      )}
      {run.summary && <p className="review-runs__summary">{run.summary}</p>}
      <p className="review-runs__muted">
        {run.findings.length} {run.findings.length === 1 ? "finding" : "findings"}
      </p>
      {run.findings.length > 0 && (
        <ul className="review-runs__findings" aria-label="Findings">
          {run.findings.map((f) => (
            <FindingRow key={f.id} finding={f} />
          ))}
        </ul>
      )}
    </li>
  );
}

// What slop-review said about the attempt's branch, by the commit it looked
// at: a verdict on an older head says nothing about the change as it is now,
// so the head the attempt is on is marked and the rest are history. Read-only:
// agents label the findings.
export default function ReviewRunsSection({ ticketId, attempt }: { ticketId: number; attempt: TicketAttempt }) {
  const runs = useReviewRuns(ticketId, attempt.number);
  const currentHead = attempt.signals?.head_sha;
  return (
    <section className="review-runs" aria-label="Reviews">
      <h2>Reviews</h2>
      {runs.status === "error" && !runs.data && (
        <p role="alert" className="review-runs__failure">
          {runs.error}
        </p>
      )}
      {runs.status === "error" && runs.data && (
        <p role="alert" className="review-runs__failure">
          Could not refresh, so this may be out of date: {runs.error}
        </p>
      )}
      {runs.status === "loading" && <p className="review-runs__muted">Loading…</p>}
      {runs.data && runs.data.length === 0 && <p className="review-runs__muted">No review has been posted for this attempt.</p>}
      {runs.data &&
        groupByHead(runs.data).map((group) => (
          <section key={group.headSha} className="review-runs__group" aria-label={`Head ${shortSha(group.headSha)}`}>
            <h3>
              <code>{shortSha(group.headSha)}</code>
              {sameHead(currentHead, group.headSha) && <span className="review-runs__badge review-runs__badge--current">current head</span>}
            </h3>
            <ul className="review-runs__runs">
              {group.runs.map((run) => (
                <RunRow key={run.id} run={run} />
              ))}
            </ul>
          </section>
        ))}
    </section>
  );
}
