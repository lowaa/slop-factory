import type { TicketAttempt } from "@/api";
import "./run_section.scss";

type Run = TicketAttempt["runs"][number];

const UNKNOWN = "not recorded";

function fleetInstructions(run: Run): string {
  return run.fleet_instructions ? `${run.fleet_instructions.name} v${run.fleet_instructions.version}` : "none served";
}

// What each lease of the attempt ran with, beside its variant: a result is only
// honest if it says what else changed besides the harness. A lease is a row
// because an attempt resumed later can run under a different release or
// workspace bundle.
export default function RunSection({ attempt }: { attempt: TicketAttempt }) {
  if (attempt.runs.length === 0) return null;
  return (
    <section className="ticket-detail__runs">
      <h2>Ran with</h2>
      {attempt.runs.map((run) => (
        <dl key={run.acquired_at} className="ticket-detail__run" role="group" aria-label={`Lease at ${run.acquired_at}`}>
          <dt>Harness</dt>
          <dd>{run.harness ? [run.harness, run.harness_version].filter(Boolean).join(" ") : UNKNOWN}</dd>
          <dt>Model</dt>
          <dd>{run.model ?? UNKNOWN}</dd>
          <dt>Workspace bundle</dt>
          <dd>{run.bundle_hash ?? UNKNOWN}</dd>
          <dt>Fleet instructions</dt>
          <dd>{fleetInstructions(run)}</dd>
        </dl>
      ))}
    </section>
  );
}
