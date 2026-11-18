import { apiClient, type LockHistoryReport } from "@/api";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import { formatDuration } from "@/lib/format_duration";
import { reportErrorMessage } from "./report_error";
import type { ReportDays } from "./report_filter";
import "./merge_wait.scss";

type Quantiles = LockHistoryReport["total"]["wait"];

const quantiles = (q: Quantiles) => (q ? `median ${formatDuration(q.p50)}, p95 ${formatDuration(q.p95)}` : "none");

// The lock history is kept 30 days, like the scope history, so a 90-day
// period shows 30 rather than a view that is mostly empty.
const MERGE_WAIT_MAX_DAYS = 30;

// MergeWaitSection is the time agents spend queued for, and holding, the
// integration lock: the merge wait of worker mode, where a ticket merging
// still reads in_progress.
export default function MergeWaitSection({ days: period }: { days: ReportDays }) {
  const days = Math.min(period, MERGE_WAIT_MAX_DAYS) as 7 | 30;
  return (
    <section className="merge-wait" aria-labelledby="reports-merge-wait">
      <h2 id="reports-merge-wait" className="merge-wait__heading">
        Merge wait
      </h2>
      {days < period && (
        <p className="merge-wait__note" data-testid="merge-wait-capped">
          Lock history is kept for {MERGE_WAIT_MAX_DAYS} days, so this shows the last {days}.
        </p>
      )}
      {/* Keyed by the period so a change starts from "loading" again. */}
      <MergeWaitView key={days} days={days} />
    </section>
  );
}

function MergeWaitView({ days }: { days: 7 | 30 }) {
  const state = usePolledResource(() => apiClient.getLockHistoryReport({ days }), {
    deps: [days],
    describeError: reportErrorMessage("merge wait report", "Failed to load the merge wait."),
  });

  return (
    <ResourceView state={state} loading="Loading merge wait…" noticeClassName="reports-panel__notice">
      {(report) => <MergeWaitBody report={report} />}
    </ResourceView>
  );
}

function MergeWaitBody({ report }: { report: LockHistoryReport }) {
  const { total } = report;
  return (
    <>
      <p className="merge-wait__note" data-testid="merge-wait-rules">
        Wait is from queueing for the integration lock to getting it; agents that found it free have none. Hold is from
        getting it to releasing it, over the holds already released.
      </p>
      {total.holds === 0 ? (
        <p className="merge-wait__note" data-testid="merge-wait-none">
          The integration lock was not taken in this period.
        </p>
      ) : (
        <dl className="merge-wait__facts">
          <Fact label="Holds" value={String(total.holds)} detail={total.ongoing > 0 ? `${total.ongoing} still held` : undefined} />
          <Fact label="Waited for the lock" value={`${total.queued} of ${total.holds}`} detail={quantiles(total.wait)} />
          <Fact label="Hold time" value={formatDuration(report.held_seconds)} detail={quantiles(total.hold)} />
        </dl>
      )}
    </>
  );
}

function Fact({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="merge-wait__fact">
      <dt>{label}</dt>
      <dd>{value}</dd>
      {detail && <dd className="merge-wait__note">{detail}</dd>}
    </div>
  );
}
