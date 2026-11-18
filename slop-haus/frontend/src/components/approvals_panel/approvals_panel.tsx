import { useMemo, useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type BatchProposalKind, type Proposal, type ProposalResult } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { isStale, useProposals } from "@/components/proposals";
import { TicketFilter } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import VirtualList from "@/shared/virtual_list/virtual_list";
import { APPROVALS_FILTER_MODEL, approvalsTicketFilter } from "@/domain/approvals_filter";
import ProposalCard from "./proposal_card";
import RejectForm from "./reject_form";
import DecidedProposals from "./decided_proposals";
import { usePendingProposals } from "./use_pending_proposals";
import "./approvals_panel.scss";

const BATCHES: { kind: BatchProposalKind; label: string }[] = [
  { kind: "ready", label: "Approve all to open" },
  { kind: "discussion_answer", label: "Approve all answers" },
];

// summary is each item's result in words, by ticket: a batch reports every
// one, so a refused item is seen rather than lost among the approved.
function summary(results: ProposalResult[], proposals: Proposal[]): string {
  const ticketOf = new Map(proposals.map((p) => [p.id, p.ticket_id]));
  const lines = results.map((r) => {
    const ticket = ticketOf.get(r.id);
    const name = ticket === undefined ? r.id : ticket === null ? "the workspace" : `#${ticket}`;
    return r.error === "" ? `${name}: ${r.state}` : `${name}: ${r.state} (${r.outcome || r.error})`;
  });
  return lines.join("; ");
}

interface ApprovalsPanelProps {
  values: FilterValues;
  // A changed filter replaces the address entry, as on the other ticket lists.
  onFilterChange: (values: FilterValues) => void;
}

// The server takes at most this many ids in one batch call.
const BATCH_IDS_MAX = 100;

async function inChunks(ids: string[], call: (chunk: string[]) => Promise<ProposalResult[]>): Promise<ProposalResult[]> {
  const results: ProposalResult[] = [];
  for (let i = 0; i < ids.length; i += BATCH_IDS_MAX) results.push(...(await call(ids.slice(i, i + BATCH_IDS_MAX))));
  return results;
}

// A card is a few hundred pixels and more with an edit's diff or an open
// reject form; rows are measured once drawn, and this is the guess before.
const CARD_HEIGHT = 240;

// ApprovalsPanel is where a person decides on what a console agent has
// proposed. Nothing in it changes a ticket until they approve: the agent's
// credential is a file any process of the user can read, so it can only ask.
//
// The list is paged and windowed: only the cards on screen are drawn, and
// further pages load on "Load more". The batch buttons and their counts act
// on the proposals loaded, never on all pending: each call names those
// proposals' ids, so one the filter hides (by default, those on parked,
// cancelled or done tickets), and one on a page nobody has asked for, is
// never decided by a batch.
export default function ApprovalsPanel({ values, onFilterChange }: ApprovalsPanelProps) {
  const { reload, proposals: allPending } = useProposals();
  const filter = useMemo(() => approvalsTicketFilter(values), [values]);
  const { status, error, proposals, hasMore, loadingMore, loadMore } = usePendingProposals(filter, allPending);
  const batch = useAsyncAction();
  const [notice, setNotice] = useState<string | null>(null);
  const [rejectingAll, setRejectingAll] = useState(false);
  const [view, setView] = useState<"pending" | "decided">("pending");
  const noStatus = filter === null;

  const decided = (message?: string) => {
    setNotice(message ?? null);
    void reload();
  };
  const approvable = (kind: BatchProposalKind) => proposals.filter((p) => p.kind === kind && !isStale(p));
  // "all" is only all there is once every page is loaded.
  const loadedWord = hasMore ? " loaded" : "";

  // A batch of several calls can fail part way, with the earlier ones
  // applied: the list is reloaded either way, so what was decided leaves it.
  const approveAll = (kind: BatchProposalKind) =>
    batch.run(async () => {
      const ids = approvable(kind).map((p) => p.id);
      try {
        const results = await inChunks(ids, (chunk) => apiClient.approveAllProposals(kind, chunk));
        decided(summary(results, proposals));
      } catch (err) {
        void reload();
        throw err;
      }
    }, "Failed to approve.");
  const rejectAll = (reason: string) =>
    batch.run(async () => {
      try {
        const results = await inChunks(
          proposals.map((p) => p.id),
          (chunk) => apiClient.rejectAllProposals(reason, chunk),
        );
        setRejectingAll(false);
        decided(`Rejected ${results.length}.`);
      } catch (err) {
        void reload();
        throw err;
      }
    }, "Failed to reject.");

  // Shared by both views: they are filtered by the same tickets.
  const header = (
    <>
      <p className="approvals__intro">
        Actions your console agents want taken as you. Nothing happens until you approve it here.
      </p>

      <div className="approvals__views" role="group" aria-label="View">
        <Button variant={view === "pending" ? "primary" : "secondary"} aria-pressed={view === "pending"} onClick={() => setView("pending")}>
          Pending
        </Button>
        <Button variant={view === "decided" ? "primary" : "secondary"} aria-pressed={view === "decided"} onClick={() => setView("decided")}>
          Decided
        </Button>
      </div>

      <TicketFilter model={APPROVALS_FILTER_MODEL} values={values} onChange={onFilterChange} />
    </>
  );

  if (view === "decided") {
    return (
      <div className="approvals">
        {header}
        <DecidedProposals filter={filter} />
      </div>
    );
  }

  return (
    <div className="approvals">
      {header}

      {status === "loading" && <p>Loading proposals…</p>}
      {status === "error" && (
        <p role="alert" className="approvals__error">
          {error}
        </p>
      )}
      {status === "loaded" && error !== null && (
        <p role="alert" className="approvals__error">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="approvals__notice">
          {notice}
        </p>
      )}
      {batch.error && (
        <p role="alert" className="approvals__error">
          {batch.error}
        </p>
      )}

      {status === "loaded" && proposals.length === 0 && (
        <p className="approvals__empty">
          {noStatus
            ? "Every status is hidden, so no tickets match."
            : allPending.length > 0
              ? `Nothing shown: ${allPending.length} pending ${allPending.length === 1 ? "proposal is" : "proposals are"} hidden by the filters.`
              : "Nothing waiting for you."}
        </p>
      )}

      {proposals.length > 0 && (
        <>
          <p className="approvals__scope">
            {hasMore
              ? `The buttons below act on the ${proposals.length} loaded, not on the proposals still to load: load more to include them.`
              : `The buttons below act on the ${proposals.length} shown${allPending.length > proposals.length ? `, not on all ${allPending.length} pending` : ""}.`}
          </p>
          <div className="approvals__toolbar">
            {BATCHES.map(({ kind, label }) => {
              const n = approvable(kind).length;
              return (
                n > 0 && (
                  <Button key={kind} variant="primary" disabled={batch.busy} onClick={() => void approveAll(kind)}>
                    {label.replace("Approve all", `Approve all${loadedWord}`)} ({n})
                  </Button>
                )
              );
            })}
            {!rejectingAll && (
              <Button disabled={batch.busy} onClick={() => setRejectingAll(true)}>
                Reject all{loadedWord} ({proposals.length})
              </Button>
            )}
          </div>
          {rejectingAll && (
            <RejectForm
              busy={batch.busy}
              confirmLabel={`Reject all${loadedWord} ${proposals.length}`}
              onConfirm={(reason) => void rejectAll(reason)}
              onCancel={() => setRejectingAll(false)}
            />
          )}
          <VirtualList
            fill
            items={proposals}
            getKey={(p) => p.id}
            renderRow={(p) => (
              <div className="approvals__slot">
                <ProposalCard proposal={p} onDecided={decided} />
              </div>
            )}
            label="Pending proposals"
            estimateRowHeight={CARD_HEIGHT}
            measureRows
            hasMore={hasMore}
            loading={loadingMore}
            onLoadMore={loadMore}
            mode="button"
          />
        </>
      )}
    </div>
  );
}
