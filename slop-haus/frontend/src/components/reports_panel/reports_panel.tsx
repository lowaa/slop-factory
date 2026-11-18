import { Dropdown } from "@slop-factory/ui";
import { apiClient, type Priority, type Report, type ReportLead, type ReportState } from "@/api";
import { formatDuration } from "@/lib/format_duration";
import { DAY_MS } from "@/lib/format_time";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import { statusLabel } from "@/domain/board_columns";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { reportErrorMessage } from "./report_error";
import { PanelErrorBoundary } from "@/lib/error_boundary/panel_error_boundary";
import LlmUsageSection from "./llm_usage_section";
import MergeWaitSection from "./merge_wait_section";
import ScopeCostSection from "./scope_cost_section";
import ShadowDecisionsSection from "./shadow_decisions_section";
import ColumnChart, { type ChartColumn } from "./column_chart";
import { REPORT_DAYS, REPORT_PRIORITIES, type ReportDays, type ReportFilter } from "./report_filter";
import WhereNow from "./where_now";
import { useUsage } from "@/usage/use_usage";
import "./reports_panel.scss";

const PRIORITY_OPTIONS = [{ value: "", label: "All priorities" }, ...REPORT_PRIORITIES.map((p) => ({ value: p, label: p }))];


// Dates are shown in UTC, the zone the server buckets days and weeks in, so
// a bar and its label never disagree about which day it is.
function utcDate(iso: string): string {
  return iso.slice(0, 10);
}

interface ReportsPanelProps {
  filter: ReportFilter;
  onChange: (filter: ReportFilter) => void;
  // The by-ticket usage list's filter, which the page keeps in its address
  // beside the report's.
  ticketValues: FilterValues;
  onTicketFilterChange: (values: FilterValues) => void;
}

// ReportsPanel is the Reports tab: how long tickets take and where they
// wait, over a chosen period. The figures are the server's; this only lays
// them out, and never shows an absent figure as zero.
export default function ReportsPanel({ filter, onChange: setFilter, ticketValues, onTicketFilterChange }: ReportsPanelProps) {
  const usage = useUsage();
  const onChange = (next: ReportFilter, changed: "reports_period" | "reports_priority" | "reports_parked") => {
    usage.event("filter_used", { filter: changed });
    setFilter(next);
  };
  return (
    <div className="reports-panel">
      <fieldset className="reports-panel__filters">
        <legend className="reports-panel__legend">Filters</legend>
        <div role="group" aria-label="Period" className="reports-panel__periods">
          {REPORT_DAYS.map((value) => {
            const days = Number(value) as ReportDays;
            return (
              <button
                key={value}
                type="button"
                className="reports-panel__period"
                aria-pressed={filter.days === days}
                onClick={() => onChange({ ...filter, days }, "reports_period")}
              >
                {days} days
              </button>
            );
          })}
        </div>
        <div className="reports-panel__priority">
          <Dropdown<string>
            label="Priority"
            value={filter.priority ?? ""}
            options={PRIORITY_OPTIONS}
            onChange={(next) => onChange({ ...filter, priority: (next || undefined) as Priority | undefined }, "reports_priority")}
          />
        </div>
        <label className="reports-panel__include-parked">
          <input
            type="checkbox"
            checked={filter.includeParked ?? false}
            onChange={(e) => onChange({ ...filter, includeParked: e.target.checked || undefined }, "reports_parked")}
          />
          Include parked
        </label>
      </fieldset>
      {/* Keyed by the filter so a change starts from "loading" again instead
          of showing the previous period's figures as if they were this one's. */}
      <PanelErrorBoundary panel="report" key={`${filter.days}/${filter.priority ?? ""}/${filter.includeParked ?? false}`}>
        <ReportView filter={filter} />
      </PanelErrorBoundary>
      {/* The priority filter is about tickets: the usage figures are the
          period's whole, and only the by-ticket list follows it. */}
      <PanelErrorBoundary panel="scope cost report" key={`scope-${filter.days}`}>
        <ScopeCostSection days={filter.days} />
      </PanelErrorBoundary>
      <PanelErrorBoundary panel="merge wait report" key={`merge-wait-${filter.days}`}>
        <MergeWaitSection days={filter.days} />
      </PanelErrorBoundary>
      <PanelErrorBoundary panel="shadow decisions report" key={`shadow-${filter.days}`}>
        <ShadowDecisionsSection days={filter.days} />
      </PanelErrorBoundary>
      <PanelErrorBoundary panel="LLM usage report" key={`usage-${filter.days}`}>
        <LlmUsageSection
          days={filter.days}
          tickets={{ priority: filter.priority, values: ticketValues, onFilterChange: onTicketFilterChange }}
        />
      </PanelErrorBoundary>
    </div>
  );
}

function ReportView({ filter }: { filter: ReportFilter }) {
  const state = usePolledResource(
    () => apiClient.getReport({ days: filter.days, priority: filter.priority, include_parked: filter.includeParked }),
    { deps: [filter.days, filter.priority, filter.includeParked], describeError: reportErrorMessage("report", "Failed to load the report.") },
  );

  return (
    <ResourceView state={state} loading="Loading report…" noticeClassName="reports-panel__notice">
      {(report) => <ReportBody report={report} priority={filter.priority} includeParked={filter.includeParked} />}
    </ResourceView>
  );
}

function ReportBody({ report, priority, includeParked }: { report: Report; priority?: Priority; includeParked?: boolean }) {
  const historyFrom = report.history_from;
  const beforeHistory = historyFrom !== null && new Date(historyFrom) > new Date(report.from);
  const noStays = report.states.every((s) => s.stays === 0);
  const empty = historyFrom !== null && report.lead.done === 0 && noStays;
  // With no history at all, every figure would be a gap, not a measurement.
  const showFigures = historyFrom !== null && !empty;

  return (
    <>
      {historyFrom === null && (
        <p className="reports-panel__notice reports-panel__notice--info" data-testid="history-note">
          There is no history yet: status changes are recorded from the first one on, so no time is shown.
        </p>
      )}
      {beforeHistory && (
        <p className="reports-panel__notice reports-panel__notice--info" data-testid="history-note">
          No history before {utcDate(historyFrom)}: status changes are recorded from then on, so earlier time is not
          shown.
        </p>
      )}
      {empty && (
        <p className="reports-panel__empty">
          Nothing was finished and no time was spent in a state in this period.
        </p>
      )}
      {showFigures && (
        <>
          <LeadSection lead={report.lead} weeks={report.lead_by_week} />
          <StatesSection states={report.states} to={report.to} />
          <ThroughputSection report={report} />
        </>
      )}
      <section className="reports-panel__section" aria-labelledby="reports-now">
        <h2 id="reports-now" className="reports-panel__heading">
          Where tickets are now
        </h2>
        <WhereNow rows={report.now} priority={priority} includeParked={includeParked} parkedNotShown={report.parked_not_shown} />
      </section>
    </>
  );
}

function Percentiles({ p50, p90 }: { p50: number; p90: number }) {
  return (
    <>
      <span className="reports-panel__figure">
        <span className="reports-panel__figure-label">p50</span> {formatDuration(p50)}
      </span>
      <span className="reports-panel__figure">
        <span className="reports-panel__figure-label">p90</span> {formatDuration(p90)}
      </span>
    </>
  );
}

function LeadSection({ lead, weeks }: { lead: ReportLead; weeks: ReportLead[] }) {
  const columns: ChartColumn[] = weeks.map((week) => {
    const start = week.week_start ? utcDate(week.week_start) : "";
    const figure = week.created_to_done;
    return {
      key: start,
      label: start.slice(5),
      values: [figure?.p50_seconds ?? null, figure?.p90_seconds ?? null],
      description: figure
        ? `Week of ${start}: p50 ${formatDuration(figure.p50_seconds)}, p90 ${formatDuration(figure.p90_seconds)}, ${week.done} done`
        : `Week of ${start}: nothing done`,
    };
  });
  return (
    <section className="reports-panel__section" aria-labelledby="reports-lead">
      <h2 id="reports-lead" className="reports-panel__heading">
        Lead time
      </h2>
      <dl className="reports-panel__lead">
        <div className="reports-panel__lead-row">
          <dt>Created to done</dt>
          <dd>
            {lead.created_to_done ? (
              <Percentiles p50={lead.created_to_done.p50_seconds} p90={lead.created_to_done.p90_seconds} />
            ) : (
              <span className="reports-panel__none">no done tickets</span>
            )}
          </dd>
        </div>
        <div className="reports-panel__lead-row">
          <dt>First claimed to done</dt>
          <dd>
            {lead.claimed_to_done ? (
              <>
                <Percentiles p50={lead.claimed_to_done.p50_seconds} p90={lead.claimed_to_done.p90_seconds} />
                <span className="reports-panel__over">
                  over {lead.claimed_done} of {lead.done} done tickets
                </span>
              </>
            ) : (
              <span className="reports-panel__none">no done ticket was claimed</span>
            )}
          </dd>
        </div>
      </dl>
      {columns.length > 0 && (
        <div className="reports-panel__chart">
          <h3 className="reports-panel__subheading">Created to done, by week</h3>
          <ColumnChart
            ariaLabel="Created to done, by week: p50 and p90"
            series={["p50", "p90"]}
            columns={columns}
          />
        </div>
      )}
    </section>
  );
}

// The state a ticket waits in for a person's answer comes first and stands
// out: that is time nobody could have worked, the one a team can most act on.
function orderedStates(states: ReportState[]): ReportState[] {
  return [...states.filter((s) => s.state === "discussion"), ...states.filter((s) => s.state !== "discussion")];
}

function StatesSection({ states, to }: { states: ReportState[]; to: string }) {
  return (
    <section className="reports-panel__section" aria-labelledby="reports-states">
      <h2 id="reports-states" className="reports-panel__heading">
        Time in each state
      </h2>
      <p className="reports-panel__note">
        A stay is counted in the period it ends in. Stays still going are counted up to {utcDate(to)}.
      </p>
      <ul className="reports-panel__states">
        {orderedStates(states).map((state) => {
          const discussion = state.state === "discussion";
          return (
            <li
              key={state.state}
              className={`reports-panel__state${discussion ? " reports-panel__state--discussion" : ""}`}
              data-state={state.state}
            >
              <div className="reports-panel__state-name">
                {statusLabel(state.state as Parameters<typeof statusLabel>[0])}
                {discussion && <span className="reports-panel__badge">waiting on a person</span>}
              </div>
              {state.duration ? (
                <div className="reports-panel__state-figures">
                  <Percentiles p50={state.duration.p50_seconds} p90={state.duration.p90_seconds} />
                </div>
              ) : (
                <div className="reports-panel__none">no stays</div>
              )}
              {state.share !== null && (
                <div className="reports-panel__share">
                  <div className="reports-panel__share-track">
                    <div className="reports-panel__share-bar" style={{ width: `${state.share * 100}%` }} />
                  </div>
                  <span className="reports-panel__share-text">{Math.round(state.share * 100)}% of time</span>
                </div>
              )}
              <div className="reports-panel__stays">
                {state.stays} {state.stays === 1 ? "stay" : "stays"}
                {state.ongoing > 0 && (
                  <span className="reports-panel__ongoing">
                    {" "}
                    includes {state.ongoing} still in this state, counted up to now
                  </span>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// The server returns only the days something was done on; the chart shows
// the quiet ones too, or a week of nothing would not look like one.
function throughputColumns(report: Report): ChartColumn[] {
  const done = new Map(report.throughput.map((d) => [utcDate(d.day), d.done]));
  const columns: ChartColumn[] = [];
  const last = Date.parse(report.to);
  const first = Date.parse(utcDate(report.from));
  for (let t = first; t <= last; t += DAY_MS) {
    const day = new Date(t).toISOString().slice(0, 10);
    const count = done.get(day) ?? 0;
    columns.push({ key: day, label: day.slice(5), values: [count], description: `${day}: ${count} done` });
  }
  return columns;
}

function ThroughputSection({ report }: { report: Report }) {
  const total = report.throughput.reduce((sum, d) => sum + d.done, 0);
  const columns = throughputColumns(report);
  return (
    <section className="reports-panel__section" aria-labelledby="reports-throughput">
      <h2 id="reports-throughput" className="reports-panel__heading">
        Throughput
      </h2>
      <p className="reports-panel__total">
        <strong>{total}</strong> {total === 1 ? "ticket" : "tickets"} done in this period
      </p>
      <ColumnChart
        ariaLabel="Tickets done per day"
        series={["done"]}
        columns={columns}
        labelEvery={Math.ceil(columns.length / 6)}
      />
    </section>
  );
}
