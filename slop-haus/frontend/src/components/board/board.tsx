import { useLayoutEffect, useRef } from "react";
import { Button } from "@slop-factory/ui";
import type { Status, Ticket } from "@/api";
import { PanelErrorBoundary } from "@/lib/error_boundary/panel_error_boundary";
import { cardKey } from "@/lib/paged_collection/card_key";
import { columnContext, shownColumn, visibleColumns } from "@/domain/board_columns";
import { TicketCard } from "@/components/ticket_card";
import VirtualList from "@/shared/virtual_list/virtual_list";
import "./board.scss";

interface BoardProps {
  tickets: readonly Ticket[];
  // The columns drawn: the statuses the board's list is fetched for (see
  // board_filter.ts), so a column Done or Parked has only while the filter
  // asks for it -- without that, an empty Done column would read as
  // "nothing is done".
  statuses: readonly Status[];
  // The tenant lands by the server's merge queue, which gives the board its
  // In merge queue column even while empty.
  mergeQueueMode?: boolean;
  // How each column's list stands, for the columns that are paged. A column
  // without an entry is complete as given.
  paging?: Partial<Record<Status, ColumnPaging>>;
  // The column a phone shows, one at a time (see shownColumn), and what
  // choosing another from the chips above it does. A wide window shows
  // every column and no chips, and ignores both.
  column?: Status;
  onColumnChange?: (status: Status) => void;
  // Set in select mode: cards toggle their ticket in and out of selected
  // rather than opening it, and each column can select or clear its own.
  selection?: BoardSelection;
}

export interface ColumnPaging {
  // The first page is on its way (or the column's query has just changed),
  // so an empty column is not yet "no tickets".
  loading: boolean;
  hasMore: boolean;
  loadingMore: boolean;
  error: string | null;
  onLoadMore: () => void;
  onRetry: () => void;
}

export interface BoardSelection {
  selected: ReadonlySet<number>;
  onToggle: (id: number) => void;
  onSelectAll: (ids: number[]) => void;
  onClear: (ids: number[]) => void;
}

// Done only grows and Parked is a backlog nobody plans, so these two are
// drawn through a virtual list: only the cards on screen are in the page.
// The live columns are short enough to draw whole.
const VIRTUAL_COLUMNS: readonly Status[] = ["done", "parked"];
const VIRTUAL_CARD_HEIGHT = 120;

// Both layouts are in the markup at once, and the stylesheet picks one:
// the chips and the one-column view only exist below the narrow
// breakpoint, so the desktop board is exactly what it was.
export default function Board({ tickets, statuses, mergeQueueMode = false, paging, column, onColumnChange, selection }: BoardProps) {
  const columns = visibleColumns(statuses, columnContext(tickets, mergeQueueMode)).map((c) => ({
    ...c,
    tickets: tickets.filter((t) => t.status === c.status),
    paging: paging?.[c.status],
  }));
  const shown = shownColumn(tickets, statuses, column, mergeQueueMode);
  const chipsRef = useRef<HTMLDivElement>(null);

  // The chips scroll sideways, so the shown column's chip can start off
  // the edge (?column=review on a fresh load). Scrolled by the row alone:
  // scrollIntoView would scroll the page too. A no-op on a wide window,
  // where the row is not laid out at all.
  useLayoutEffect(() => {
    const row = chipsRef.current;
    const chip = row?.querySelector(".board__chip--selected");
    if (!row || !chip) return;
    const rowBox = row.getBoundingClientRect();
    const chipBox = chip.getBoundingClientRect();
    if (chipBox.left < rowBox.left) row.scrollLeft -= rowBox.left - chipBox.left;
    else if (chipBox.right > rowBox.right) row.scrollLeft += chipBox.right - rowBox.right;
  }, [shown]);

  return (
    <div className="board">
      <div className="board__chips" ref={chipsRef} role="group" aria-label="Board column">
        {columns.map(({ status, label, tickets: inColumn, paging: columnPaging }) => (
          <button
            key={status}
            type="button"
            className={`board__chip${status === shown ? " board__chip--selected" : ""}`}
            aria-pressed={status === shown}
            onClick={() => onColumnChange?.(status)}
          >
            {/* The space is for the accessible name ("Open 3"); the flex
                layout drops it on screen. */}
            {label}{" "}
            <span className="board__chip-count">{countLabel(inColumn.length, columnPaging)}</span>
          </button>
        ))}
      </div>

      <div className="board__columns">
        {columns.map(({ status, label, tickets: inColumn, paging: columnPaging }) => (
          <div className={`board__column${status === shown ? " board__column--shown" : ""}`} key={status}>
            <div className="board__column-header">
              <h2>{label}</h2>
              <span className="board__column-count">{countLabel(inColumn.length, columnPaging)}</span>
            </div>
            {/* Beside the header rather than in it: the phone board hides
                the header, and the chip above it has no room for these. */}
            {selection && inColumn.length > 0 && (
              <ColumnSelectActions label={label} ids={[...new Set(inColumn.map((t) => t.id))]} partial={columnPaging?.hasMore === true} selection={selection} />
            )}
            <PanelErrorBoundary panel={`${label} column`}>
              <ColumnCards label={label} status={status} tickets={inColumn} paging={columnPaging} selection={selection} />
            </PanelErrorBoundary>
          </div>
        ))}
      </div>
    </div>
  );
}

// A column only holds what has been loaded, so a count with more to come is
// a lower bound: "50+" rather than a number that reads as the total.
function countLabel(loaded: number, paging: ColumnPaging | undefined): string {
  return paging?.hasMore ? `${loaded}+` : String(loaded);
}

interface ColumnCardsProps {
  label: string;
  status: Status;
  tickets: readonly Ticket[];
  paging?: ColumnPaging;
  selection?: BoardSelection;
}

function ColumnCards({ label, status, tickets, paging, selection }: ColumnCardsProps) {
  const renderCard = (ticket: Ticket) => (
    <TicketCard
      key={cardKey(ticket)}
      ticket={ticket}
      selection={
        selection && {
          selected: selection.selected.has(ticket.id),
          onToggle: () => selection.onToggle(ticket.id),
        }
      }
    />
  );
  const failed = paging?.error != null && (
    <div role="alert" className="board__column-notice">
      {paging.error}{" "}
      <Button size="sm" onClick={paging.onRetry}>
        Try again
      </Button>
    </div>
  );

  if (VIRTUAL_COLUMNS.includes(status) && tickets.length > 0) {
    return (
      <div className="board__column-cards">
        <VirtualList
          items={tickets}
          getKey={cardKey}
          renderRow={(ticket) => <div className="board__virtual-card">{renderCard(ticket)}</div>}
          label={`${label} tickets`}
          estimateRowHeight={VIRTUAL_CARD_HEIGHT}
          measureRows
          hasMore={paging?.hasMore}
          loading={paging?.loadingMore}
          onLoadMore={paging?.onLoadMore}
          mode="button"
        />
        {failed}
      </div>
    );
  }

  return (
    <div className="board__column-cards">
      {tickets.map(renderCard)}
      {tickets.length === 0 && !failed && (
        <p className="board__empty" role={paging?.loading ? "status" : undefined}>
          {paging?.loading ? "Loading…" : "No tickets"}
        </p>
      )}
      {failed}
      {paging?.hasMore && (
        <Button size="sm" disabled={paging.loadingMore} onClick={paging.onLoadMore}>
          {paging.loadingMore ? "Loading..." : "Load more"}
        </Button>
      )}
    </div>
  );
}

// partial is a column with more to load: "all" would mean only what is
// loaded, so the action says so.
function ColumnSelectActions({ label, ids, partial, selection }: { label: string; ids: number[]; partial: boolean; selection: BoardSelection }) {
  const picked = ids.filter((id) => selection.selected.has(id)).length;
  return (
    <div className="board__column-select">
      <Button
        variant="ghost"
        size="sm"
        aria-label={partial ? `Select all loaded in ${label}` : `Select all in ${label}`}
        disabled={picked === ids.length}
        onClick={() => selection.onSelectAll(ids)}
      >
        Select all
      </Button>
      <Button
        variant="ghost"
        size="sm"
        aria-label={`Clear ${label}`}
        disabled={picked === 0}
        onClick={() => selection.onClear(ids)}
      >
        Clear
      </Button>
    </div>
  );
}
