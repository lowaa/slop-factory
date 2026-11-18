import { apiClient, type ShadowDecisionsReport } from "@/api";
import { formatCount } from "@/lib/format_usage";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { reportErrorMessage } from "./report_error";
import type { ReportDays } from "./report_filter";
import "./shadow_decisions.scss";

// ShadowDecisionsSection is what the independent-apps merge rule would have
// decided on changes whose head was behind the base, and whether the checks of
// the accepted ones later passed. The rule is not enforced; this is the
// evidence for whether it should be.
// The report offers 7 and 30 days, as the lock hold and scope cost reports do,
// so a longer period shows 30.
const SHADOW_DECISIONS_MAX_DAYS = 30;

export default function ShadowDecisionsSection({ days: period }: { days: ReportDays }) {
  const days = Math.min(period, SHADOW_DECISIONS_MAX_DAYS) as 7 | 30;
  return (
    <section
      className="shadow-decisions"
      aria-labelledby="reports-shadow-decisions"
    >
      <h2 id="reports-shadow-decisions" className="shadow-decisions__heading">
        Independent-apps merge rule (shadow)
      </h2>
      {days < period && (
        <p className="reports-panel__notice" data-testid="shadow-decisions-capped">
          This report covers at most {SHADOW_DECISIONS_MAX_DAYS} days, so this shows the last {days}.
        </p>
      )}
      {/* Keyed by the period so a change starts from "loading" again. */}
      <ShadowDecisionsView key={days} days={days} />
    </section>
  );
}

function ShadowDecisionsView({ days }: { days: 7 | 30 }) {
  const state = usePolledResource(() => apiClient.getShadowDecisionsReport({ days }), {
    deps: [days],
    describeError: reportErrorMessage("shadow decisions report", "Failed to load the shadow decisions."),
  });

  return (
    <ResourceView state={state} loading="Loading shadow decisions…" noticeClassName="reports-panel__notice">
      {(report) => <ShadowDecisionsBody report={report} />}
    </ResourceView>
  );
}

function ShadowDecisionsBody({ report }: { report: ShadowDecisionsReport }) {
  if (report.would_accept + report.would_refuse === 0) {
    return (
      <p className="shadow-decisions__note" data-testid="shadow-decisions-none">
        No change was merged behind its base in this period.
      </p>
    );
  }
  return (
    <>
      <p className="shadow-decisions__note">
        Decisions the rule would have made while the gate refused every such
        head. A would-accept counts as passed or failed by the checks of the
        later head that took the base; until they conclude it is waiting.
      </p>
      <dl className="shadow-decisions__facts">
        <Fact label="Would accept" value={formatCount(report.would_accept)} />
        <Fact label="Would refuse" value={formatCount(report.would_refuse)} />
        <Fact
          label="Accepted, checks passed"
          value={formatCount(report.accept_ci_passed)}
        />
        <Fact
          label="Accepted, checks failed"
          value={formatCount(report.accept_ci_failed)}
        />
        <Fact
          label="Accepted, awaiting checks"
          value={formatCount(report.accept_awaiting_ci)}
        />
      </dl>
      {report.reasons.length > 0 && (
        <div
          className="shadow-decisions__block"
          data-testid="shadow-decisions-reasons"
        >
          <h3 className="reports-panel__subheading">Reasons</h3>
          <ul className="shadow-decisions__list">
            {report.reasons.map((r) => (
              <li
                key={`${r.decision}/${r.reason}`}
                className="shadow-decisions__row"
              >
                <span>
                  {r.decision === "would_accept" ? "accept" : "refuse"}:{" "}
                  {r.reason}
                </span>
                <span className="shadow-decisions__value">
                  {formatCount(r.count)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div
        className="shadow-decisions__block"
        data-testid="shadow-decisions-failures"
      >
        <h3 className="reports-panel__subheading">
          Accepted, then checks failed
        </h3>
        {report.failures.length === 0 ? (
          <p className="shadow-decisions__note">None.</p>
        ) : (
          <ul className="shadow-decisions__list">
            {report.failures.map((f) => (
              <li
                key={`${f.change_id}/${f.head_sha}`}
                className="shadow-decisions__row"
              >
                <TicketLink ticketId={f.ticket_id} from="reports">
                  #{f.ticket_id} {f.title}
                </TicketLink>
                <span className="shadow-decisions__value">
                  {f.drift_dirs.join(", ")}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="shadow-decisions__fact">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}
