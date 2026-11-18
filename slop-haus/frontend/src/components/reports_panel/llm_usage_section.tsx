import { useState } from "react";
import { apiClient, type LlmUsageReport, type LlmUsageSums } from "@/api";
import { formatDuration } from "@/lib/format_duration";
import { DAY_MS } from "@/lib/format_time";
import { formatCost, formatCount } from "@/lib/format_usage";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import ColumnChart, { type ChartColumn } from "./column_chart";
import Cost from "./llm_usage_cost";
import { costKind, modeLabel } from "@/domain/llm_usage_labels";
import UsageTickets, { type UsageTicketsFilterProps } from "./llm_usage_tickets";
import { reportErrorMessage } from "./report_error";
import type { ReportDays } from "./report_filter";
import "./llm_usage.scss";


const tokensOf = (s: LlmUsageSums) => s.input_tokens + s.output_tokens + s.cache_read_tokens + s.cache_write_tokens;

// LlmUsageSection is what agents used over the period: requests, tokens and
// cost. Cost is only ever shown per billing mode -- a subscription's is an
// estimate and a metered one is money spent, so the two are never added.
export default function LlmUsageSection({ days, tickets }: { days: ReportDays; tickets: UsageTicketsFilterProps }) {
  return (
    <section className="llm-usage" aria-labelledby="reports-llm-usage">
      <h2 id="reports-llm-usage" className="llm-usage__heading">
        LLM usage
      </h2>
      {/* Keyed by the period so a change starts from "loading" again rather
          than showing the previous period's figures as this one's. */}
      <UsageView key={days} days={days} tickets={tickets} />
    </section>
  );
}

function UsageView({ days, tickets }: { days: ReportDays; tickets: UsageTicketsFilterProps }) {
  const [includeUnreliable, setIncludeUnreliable] = useState(false);
  const state = usePolledResource(
    () => apiClient.getLlmUsageReport({ days, ...(includeUnreliable && { include_unreliable: true }) }),
    { deps: [days, includeUnreliable], describeError: reportErrorMessage("usage report", "Failed to load LLM usage.") },
  );

  return (
    <ResourceView state={state} loading="Loading LLM usage…" noticeClassName="reports-panel__notice">
      {(report) => (
        <UsageBody
          report={report}
          days={days}
          tickets={tickets}
          includeUnreliable={includeUnreliable}
          onIncludeUnreliable={setIncludeUnreliable}
        />
      )}
    </ResourceView>
  );
}

interface UsageBodyProps {
  report: LlmUsageReport;
  days: ReportDays;
  tickets: UsageTicketsFilterProps;
  includeUnreliable: boolean;
  onIncludeUnreliable: (include: boolean) => void;
}

function UsageBody({ report, days, tickets, includeUnreliable, onIncludeUnreliable }: UsageBodyProps) {
  const notice = <OvercountNotice report={report} included={includeUnreliable} onToggle={onIncludeUnreliable} />;
  if (report.totals.length === 0) {
    return (
      <>
        {notice}
        <p className="llm-usage__note">No LLM usage was reported in this period.</p>
      </>
    );
  }
  return (
    <>
      {notice}
      <p className="llm-usage__note" data-testid="llm-usage-rules">
        Usage is counted in the period its transcript last ran: a transcript still going moves to a later period as it
        reports more. Subscription cost is an estimate of what the usage would cost at API prices, not money spent;
        metered cost is money spent. The two are never added together, and usage with no price is shown as unpriced.
      </p>
      <Totals report={report} />
      <Trend report={report} days={days} />
      <Breakdown
        title="By agent"
        rows={report.agents.map((a) => ({ key: `${a.agent_id}/${a.billing_mode}`, name: a.name || "(removed agent)", sums: a }))}
      />
      <Breakdown
        title="By model"
        rows={report.models.map((m) => ({ key: `${m.provider}/${m.model}/${m.billing_mode}`, name: m.model, sub: m.provider, sums: m }))}
      />
      <ByTicket report={report} days={days} tickets={tickets} includeUnreliable={includeUnreliable} />
      <Tools tools={report.tools} />
    </>
  );
}

// Usage a conductor or a headless slot reported before the over-count fix is
// flagged on the server and left out unless asked for. The cutoff's own day is
// flagged whole: the daily totals can't be split by report time, and hosts ran
// the old code for hours after the deploy. Shown only when the period holds
// some, so it never nags about nothing.
function OvercountNotice({ report, included, onToggle }: { report: LlmUsageReport; included: boolean; onToggle: (include: boolean) => void }) {
  if (report.unreliable_records === 0) return null;
  const before = report.unreliable_before.slice(0, 10);
  return (
    <p className="llm-usage__note" data-testid="overcount-notice">
      {included ? `Usage up to ${before} that was over-counted is included.` : `Some usage up to ${before} was over-counted and is left out (that day whole, as it cannot be split by report time).`}{" "}
      <label>
        <input className="llm-usage__toggle" type="checkbox" checked={included} onChange={(e) => onToggle(e.target.checked)} /> Include over-counted usage
      </label>
    </p>
  );
}

function Totals({ report }: { report: LlmUsageReport }) {
  return (
    <div className="llm-usage__block">
      <h3 className="reports-panel__subheading">Totals</h3>
      <ul className="llm-usage__totals">
        {report.totals.map((t) => {
          const loose = report.unattributed.find((u) => u.billing_mode === t.billing_mode);
          const guided = report.guidance.find((u) => u.billing_mode === t.billing_mode);
          return (
            <li key={t.billing_mode} className="llm-usage__total" data-mode={t.billing_mode}>
              <div className="llm-usage__mode-name">
                {modeLabel(t.billing_mode)} <span className="llm-usage__kind">cost is {costKind(t.billing_mode)}</span>
              </div>
              <div className="llm-usage__cost-big">
                <Cost sums={t} detail />
              </div>
              <dl className="llm-usage__facts">
                <div>
                  <dt>Transcripts</dt>
                  <dd>{formatCount(t.transcripts ?? 0)}</dd>
                </div>
                <div>
                  <dt>Requests</dt>
                  <dd>{formatCount(t.requests)}</dd>
                </div>
                <div>
                  <dt>Input</dt>
                  <dd>{formatCount(t.input_tokens)}</dd>
                </div>
                <div>
                  <dt>Output</dt>
                  <dd>
                    {formatCount(t.output_tokens)}{" "}
                    <span className="llm-usage__note">({formatCount(t.reasoning_tokens)} reasoning)</span>
                  </dd>
                </div>
                <div>
                  <dt>Cache read</dt>
                  <dd>{formatCount(t.cache_read_tokens)}</dd>
                </div>
                <div>
                  <dt>Cache write</dt>
                  <dd>{formatCount(t.cache_write_tokens)}</dd>
                </div>
              </dl>
              {loose && (
                <p className="llm-usage__loose" data-testid="unattributed">
                  Not tied to a ticket: {formatCount(loose.requests)} requests, {formatCount(tokensOf(loose))} tokens,{" "}
                  <Cost sums={loose} />
                </p>
              )}
              {guided && (
                <p className="llm-usage__loose" data-testid="guidance">
                  Guidance charged to tickets (part of the totals): {formatCount(guided.requests)} requests,{" "}
                  {formatCount(tokensOf(guided))} tokens, <Cost sums={guided} />
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// The server returns only the days with usage; the chart shows the quiet
// ones too, or a week of nothing would not look like one.
function dayColumns(report: LlmUsageReport, value: (rows: LlmUsageReport["days"]) => number | null, describe: (day: string, v: number | null) => string): ChartColumn[] {
  const byDay = new Map<string, LlmUsageReport["days"]>();
  for (const d of report.days) {
    const key = d.day.slice(0, 10);
    byDay.set(key, [...(byDay.get(key) ?? []), d]);
  }
  const columns: ChartColumn[] = [];
  const last = Date.parse(report.to);
  for (let t = Date.parse(report.from.slice(0, 10)); t <= last; t += DAY_MS) {
    const day = new Date(t).toISOString().slice(0, 10);
    const v = value(byDay.get(day) ?? []);
    columns.push({ key: day, label: day.slice(5), values: [v], description: describe(day, v) });
  }
  return columns;
}

function Trend({ report, days }: { report: LlmUsageReport; days: ReportDays }) {
  const every = Math.ceil((days + 1) / 6);
  const tokens = dayColumns(
    report,
    (rows) => rows.reduce((sum, r) => sum + tokensOf(r), 0),
    (day, v) => `${day}: ${formatCount(v ?? 0)} tokens`,
  );
  return (
    <div className="llm-usage__block">
      <h3 className="reports-panel__subheading">Tokens per day</h3>
      <ColumnChart ariaLabel="Tokens per day" series={["tokens"]} columns={tokens} labelEvery={every} />
      {report.totals.map((t) => {
        const mode = t.billing_mode;
        const cost = dayColumns(
          report,
          (rows) => {
            const priced = rows.filter((r) => r.billing_mode === mode && r.cost_micros !== null);
            return priced.length ? priced.reduce((sum, r) => sum + (r.cost_micros ?? 0), 0) : null;
          },
          (day, v) => `${day}: ${v === null ? "no priced usage" : `${formatCost(v)} ${costKind(mode)}`}`,
        );
        return (
          <div key={mode} className="llm-usage__chart">
            <h3 className="reports-panel__subheading">
              {modeLabel(mode)} cost per day ({costKind(mode)})
            </h3>
            <ColumnChart ariaLabel={`${modeLabel(mode)} cost per day, ${costKind(mode)}`} series={["cost"]} columns={cost} labelEvery={every} />
          </div>
        );
      })}
    </div>
  );
}

interface BreakdownRow {
  key: string;
  name: string;
  sub?: string;
  sums: LlmUsageSums;
}

function Breakdown({ title, rows }: { title: string; rows: BreakdownRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="llm-usage__block">
      <h3 className="reports-panel__subheading">{title}</h3>
      <div className="llm-usage__table-wrap">
        <table className="llm-usage__table">
          <thead>
            <tr>
              <th scope="col">{title.replace("By ", "").replace(/^./, (c) => c.toUpperCase())}</th>
              <th scope="col">Billing</th>
              <th scope="col" className="llm-usage__num">
                Requests
              </th>
              <th scope="col" className="llm-usage__num">
                Tokens
              </th>
              <th scope="col">Cost</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key}>
                <th scope="row">
                  {r.name}
                  {r.sub && <span className="llm-usage__note"> {r.sub}</span>}
                </th>
                <td>{modeLabel(r.sums.billing_mode)}</td>
                <td className="llm-usage__num">{formatCount(r.sums.requests)}</td>
                <td className="llm-usage__num">{formatCount(tokensOf(r.sums))}</td>
                <td>
                  <Cost sums={r.sums} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function ByTicket({
  report,
  days,
  tickets,
  includeUnreliable,
}: {
  report: LlmUsageReport;
  days: ReportDays;
  tickets: UsageTicketsFilterProps;
  includeUnreliable: boolean;
}) {
  return (
    <div className="llm-usage__block">
      <h3 className="reports-panel__subheading">By ticket</h3>
      {report.done_tickets.length > 0 && (
        <ul className="llm-usage__done" data-testid="done-tickets">
          {report.done_tickets.map((d) => (
            <li key={d.billing_mode}>
              {modeLabel(d.billing_mode)}: {d.tickets} done {d.tickets === 1 ? "ticket" : "tickets"} with usage
              {d.cost_micros !== null && d.cost_p50_micros !== null ? (
                <>
                  , median {formatCost(d.cost_p50_micros)} {costKind(d.billing_mode)} each, {formatCost(d.cost_micros)}{" "}
                  {costKind(d.billing_mode)} in all
                </>
              ) : (
                <>, unpriced</>
              )}
              {d.unpriced_tickets > 0 && (
                <span className="llm-usage__note">
                  {" "}
                  ({d.unpriced_tickets} with usage that has no price, so understated)
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {report.tickets === 0 ? (
        <p className="llm-usage__note">No ticket has usage in this period.</p>
      ) : (
        <UsageTickets days={days} includeUnreliable={includeUnreliable} {...tickets} />
      )}
    </div>
  );
}

function Tools({ tools }: { tools: LlmUsageReport["tools"] }) {
  if (tools.length === 0) return null;
  return (
    <div className="llm-usage__block">
      <h3 className="reports-panel__subheading">Tool use by category</h3>
      <div className="llm-usage__table-wrap">
        <table className="llm-usage__table">
          <thead>
            <tr>
              <th scope="col">Category</th>
              <th scope="col" className="llm-usage__num">
                Calls
              </th>
              <th scope="col" className="llm-usage__num">
                Error rate
              </th>
              <th scope="col" className="llm-usage__num">
                Time
              </th>
            </tr>
          </thead>
          <tbody>
            {tools.map((t) => (
              <tr key={t.category}>
                <th scope="row">{t.category}</th>
                <td className="llm-usage__num">{formatCount(t.calls)}</td>
                <td className="llm-usage__num">{t.calls === 0 ? "-" : `${((t.errors / t.calls) * 100).toFixed(1)}%`}</td>
                <td className="llm-usage__num">{formatDuration(t.duration_ms / 1000)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
