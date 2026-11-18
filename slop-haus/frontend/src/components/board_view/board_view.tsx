import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@slop-factory/ui";
import type { Status, Ticket } from "@/api";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import { apiClient } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import { Board, type ColumnPaging } from "@/components/board";
import { TicketFilter } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { BulkActionBar } from "@/components/bulk_action_bar";
import { BOARD_FILTER_MODEL, BOARD_SORT_MODEL, boardColumnStatuses } from "@/domain/board_filter";
import { IntegrationLockPanel } from "@/components/integration_lock";
import { LandingQueueProvider, MergeLockProvider } from "@/landing_state";
import { NewTicketForm } from "@/components/new_ticket_form";
import { SetupChecklist } from "@/components/onboarding";
import { PausedSlotsNotice } from "@/components/paused_slots_notice";
import { QueueBlockagePanel } from "@/components/queue_blockage";
import { BoardTicketFeed } from "./board_ticket_feed";
import { useBoardColumns } from "./use_board_columns";
import { useBulkSelection } from "./use_bulk_selection";
import { useLiveRefresh, type LiveRefreshScope } from "@/lib/live";
import "./board_view.scss";

// BoardView owns the ticket lists, rather than the app shell, so they are
// fetched each time the board mounts -- like every other tab. Tickets change
// under the board from several places (the detail view, agents working the
// queue), and a list held above the tabs is stale after any of them unless
// every way back to the board (Back, the header's Board tab, the account
// page) remembers to refetch.
//
// Each column is its own paged list (see useBoardColumns), so a column that
// has grown long loads more on its own, and a filter change starts them all
// again from the top.
//
// The filter is the caller's -- BoardRoute keeps it in the address (?done=,
// ?parked=, ?q=), so a filtered board survives a reload and can be shared as a link.
interface BoardViewProps {
  filterValues: FilterValues;
  onFilterChange: (values: FilterValues) => void;
  // One sort for every column (?sort=, left out at the default).
  sort: SortId;
  onSortChange: (sort: SortId) => void;
  // The column a phone shows (?column=) and what choosing another does;
  // see Board.
  column?: Status;
  onColumnChange?: (status: Status) => void;
}

export default function BoardView({ filterValues, onFilterChange, sort, onSortChange, column, onColumnChange }: BoardViewProps) {
  const feed = useMemo(() => new BoardTicketFeed(), []);
  const lists = useBoardColumns(filterValues, sort, feed.subscribe);
  const statuses = useMemo(() => boardColumnStatuses(filterValues), [filterValues]);
  // Unknown (still loading, or failed) reads as worker mode: the In merge
  // queue column then shows only if a ticket is in it.
  const capabilities = usePolledResource(() => apiClient.getLandingCapabilities());
  const mergeQueueMode = capabilities.data?.merge_method === "merge_queue";

  const tickets = useMemo(() => statuses.flatMap((status) => lists[status].items), [statuses, lists]);
  const paging = useMemo(
    () =>
      Object.fromEntries(
        statuses.map((status): [Status, ColumnPaging] => {
          const list = lists[status];
          return [
            status,
            {
              loading: !list.loaded,
              hasMore: list.hasMore,
              loadingMore: list.loadingMore,
              error: list.error,
              onLoadMore: list.loadMore,
              onRetry: list.retry,
            },
          ];
        }),
      ),
    [statuses, lists],
  );

  // Selection is held here, above the board, so it outlives each reload
  // (and the phone board's switching between columns); a ticket the lists
  // no longer have is dropped from it. Until every column has its first
  // page, and none is mid-reload, the tickets are a mix of old and new
  // pages, and pruning against them would drop a ticket that is moving
  // between a column whose reply has landed and one whose reply has not.
  const allLoaded = statuses.every((status) => lists[status].loaded && !lists[status].loading);
  const loadedIds = useMemo(() => (allLoaded ? tickets.map((t) => t.id) : undefined), [allLoaded, tickets]);
  const selection = useBulkSelection<number>(loadedIds);
  // Selection and the bulk actions are per ticket, not per card: a ticket
  // with several attempts is several cards in the work columns, but one
  // thing to move, prioritise or approve. Selecting either card selects the
  // ticket (and so marks all of its cards), and the actions below act on it
  // once, so the bar's count is of tickets.
  const selectedTickets = useMemo(() => {
    const seen = new Set<number>();
    return tickets.filter((t: Ticket) => selection.selected.has(t.id) && !seen.has(t.id) && seen.add(t.id));
  }, [tickets, selection.selected]);

  // The lock and queue-blockage panels are fetched when the board is, on the
  // same live stream: the server moves the board's revision when the lock,
  // its queue or a lease changes, so a change refetches them all.
  const [lockRefresh, setLockRefresh] = useState(0);

  // Reloading goes through the latest lists, so a stream change or a bulk
  // action finishing reloads the columns as they are now, with the filter
  // they now have.
  const listsRef = useRef(lists);
  useEffect(() => {
    listsRef.current = lists;
  }, [lists]);
  const reloadColumns = useCallback(() => {
    for (const list of Object.values(listsRef.current)) list.reload();
  }, []);

  // Kept current as tickets change elsewhere, in place: no reset to
  // "loading", the filter being typed and the phone's column left alone. The
  // stream names the tickets that changed, which each column applies in
  // place (PagedCollection.applyTicket), keeping the pages it has loaded; a
  // change it cannot name reloads each column's first page instead, which
  // drops pages beyond it. A selected ticket that moves stays selected; one
  // that leaves the lists is dropped from the selection, as
  // useBulkSelection does.
  const refreshQuietly = useCallback(
    ({ columns }: LiveRefreshScope) => {
      if (columns) reloadColumns();
      setLockRefresh((n) => n + 1);
    },
    [reloadColumns],
  );
  const applyTickets = useCallback((changed: Ticket[], cards: Ticket[]) => feed.emit(changed, cards), [feed]);
  useLiveRefresh(refreshQuietly, undefined, applyTickets);

  const { exit: exitSelection } = selection;
  const handleApplied = useCallback(() => {
    exitSelection();
    reloadColumns();
  }, [exitSelection, reloadColumns]);

  return (
    <div className="board-view">
      <SetupChecklist refreshToken={lockRefresh} />
      <PausedSlotsNotice refreshToken={lockRefresh} />
      <QueueBlockagePanel refreshToken={lockRefresh} />
      <IntegrationLockPanel refreshToken={lockRefresh} />
      <NewTicketForm onCreated={reloadColumns} />
      <div className="board-view__toolbar">
        <TicketFilter
          model={BOARD_FILTER_MODEL}
          values={filterValues}
          onChange={onFilterChange}
          sort={{ model: BOARD_SORT_MODEL, value: sort, onChange: onSortChange }}
        />
        <Button
          className="board-view__select-toggle"
          aria-pressed={selection.active}
          onClick={selection.active ? selection.exit : selection.enter}
        >
          Select
        </Button>
      </div>

      <LandingQueueProvider refreshToken={lockRefresh}>
        <MergeLockProvider refreshToken={lockRefresh}>
        <Board
          tickets={tickets}
          statuses={statuses}
          mergeQueueMode={mergeQueueMode}
          paging={paging}
          column={column}
          onColumnChange={onColumnChange}
          selection={
            selection.active
              ? {
                  selected: selection.selected,
                  onToggle: selection.toggle,
                  onSelectAll: selection.selectAll,
                  onClear: selection.deselect,
                }
              : undefined
          }
        />
        </MergeLockProvider>
      </LandingQueueProvider>
      {selection.active && (
        <BulkActionBar
          selected={selectedTickets}
          onApplied={handleApplied}
          onRefused={reloadColumns}
          onDeselect={selection.deselect}
          onExit={selection.exit}
        />
      )}
    </div>
  );
}
