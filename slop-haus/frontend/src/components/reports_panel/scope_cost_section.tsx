import { apiClient, type ScopeCostReport } from "@/api";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import { formatDuration } from "@/lib/format_duration";
import { formatCount } from "@/lib/format_usage";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { reportErrorMessage } from "./report_error";
import type { ReportDays } from "./report_filter";
import "./scope_cost.scss";

const orNone = (seconds: number | null | undefined) => (seconds == null ? "none" : formatDuration(seconds));

// ScopeCostSection is what exclusive path scopes cost the fleet over the
// period: tickets held back by another ticket's scope, how long they waited,
// and how much of that time a worker sat idle.
// The history behind it is kept 30 days, so a longer period shows 30 rather
// than a 90-day view that is mostly empty.
const SCOPE_COST_MAX_DAYS = 30;

export default function ScopeCostSection({ days: period }: { days: ReportDays }) {
  const days = Math.min(period, SCOPE_COST_MAX_DAYS) as 7 | 30;
  return (
    <section className="scope-cost" aria-labelledby="reports-scope-cost">
      <h2 id="reports-scope-cost" className="scope-cost__heading">
        Scope cost
      </h2>
      {days < period && (
        <p className="scope-cost__note" data-testid="scope-cost-capped">
          Scope history is kept for {SCOPE_COST_MAX_DAYS} days, so this shows the last {days}.
        </p>
      )}
      {/* Keyed by the period so a change starts from "loading" again. */}
      <ScopeCostView key={days} days={days} />
    </section>
  );
}

function ScopeCostView({ days }: { days: 7 | 30 }) {
  const state = usePolledResource(() => apiClient.getScopeCostReport({ days }), {
    deps: [days],
    describeError: reportErrorMessage("scope cost report", "Failed to load the scope cost."),
  });

  return (
    <ResourceView state={state} loading="Loading scope cost…" noticeClassName="reports-panel__notice">
      {(report) => <ScopeCostBody report={report} />}
    </ResourceView>
  );
}

function ScopeCostBody({ report }: { report: ScopeCostReport }) {
  return (
    <>
      {report.locking_off && (
        <p className="scope-cost__note" data-testid="scope-cost-locking-off">
          Scope locking is off: no ticket is held back or denied by a scope now, so an empty report here does not mean
          scopes do not overlap.
        </p>
      )}
      <p className="scope-cost__note" data-testid="scope-cost-rules">
        A ticket is held back when a claim finds it ready but its paths overlap a scope another ticket holds. A wait still
        going counts up to the end of the period. Idle capacity is time a worker had no ticket while one was held back; it
        counts every worker as available from its creation, including while processing was switched off.
      </p>
      {report.intervals === 0 && report.waits.tickets === 0 ? (
        <p className="scope-cost__note" data-testid="scope-cost-none">
          No ticket was held back by a scope in this period.
        </p>
      ) : (
        <>
          <dl className="scope-cost__facts">
            <Fact label="Tickets held back" value={formatCount(report.waits.tickets)} detail={`${report.waits.claimed} since claimed, ${report.waits.ongoing} still held back`} />
            <Fact label="Total wait" value={formatDuration(report.waits.total_seconds)} detail={`median ${orNone(report.waits.median_seconds)}`} />
            <Fact label="Any ticket held back" value={formatDuration(report.blocked_seconds)} />
            <Fact label="Idle worker while held back" value={formatDuration(report.idle_blocked_seconds)} />
          </dl>
          <Ranked
            title="Tickets held back longest"
            rows={report.tickets.map((t) => ({
              key: String(t.id),
              name: <TicketLink ticketId={t.id} from="reports">#{t.id} {t.title}</TicketLink>,
              value: formatDuration(t.seconds) + (t.ongoing ? " (ongoing)" : ""),
            }))}
          />
          <Ranked
            title="Tickets holding others back"
            rows={report.blockers.map((b) => ({
              key: String(b.id),
              name: <TicketLink ticketId={b.id} from="reports">#{b.id} {b.title}</TicketLink>,
              value: `${formatDuration(b.seconds)} across ${b.tickets} ${b.tickets === 1 ? "ticket" : "tickets"}`,
            }))}
          />
          <Ranked
            title="Paths holding others back"
            rows={report.paths.map((p) => ({
              key: p.path,
              name: <code>{p.path}</code>,
              value: `${formatDuration(p.seconds)} across ${p.tickets} ${p.tickets === 1 ? "ticket" : "tickets"}`,
            }))}
          />
        </>
      )}
      <div className="scope-cost__block" data-testid="scope-cost-denials">
        <h3 className="reports-panel__subheading">Scope extensions refused</h3>
        {report.denials.total === 0 ? (
          <p className="scope-cost__note">None in this period.</p>
        ) : (
          <>
            <p className="scope-cost__note">
              {report.denials.total} refused: {report.denials.granted} later granted, {report.denials.narrowed} narrowed,{" "}
              {report.denials.gave_up} gave up, {report.denials.open} still open. Median time to resolve{" "}
              {orNone(report.denials.median_seconds_to_resolve)}.
            </p>
            <Ranked
              title="Paths refused most"
              rows={report.denials.paths.map((p) => ({ key: p.path, name: <code>{p.path}</code>, value: String(p.count) }))}
            />
          </>
        )}
      </div>
      <p className="scope-cost__note" data-testid="scope-cost-narrowings">
        Agents narrowed their scope {report.narrowings} {report.narrowings === 1 ? "time" : "times"} in this period.
      </p>
    </>
  );
}

function Fact({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="scope-cost__fact">
      <dt>{label}</dt>
      <dd>{value}</dd>
      {detail && <dd className="scope-cost__note">{detail}</dd>}
    </div>
  );
}

function Ranked({ title, rows }: { title: string; rows: { key: string; name: React.ReactNode; value: string }[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="scope-cost__block">
      <h3 className="reports-panel__subheading">{title}</h3>
      <ul className="scope-cost__list">
        {rows.map((r) => (
          <li key={r.key} className="scope-cost__row">
            <span className="scope-cost__name">{r.name}</span>
            <span className="scope-cost__value">{r.value}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
