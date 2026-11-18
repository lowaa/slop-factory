import type { ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import type { Ticket } from "@/api";
import type { PagedCollectionView } from "@/lib/paged_collection/use_paged_collection";
import type { TabName } from "@/tabs";
import TicketLink from "@/shared/ticket_link/ticket_link";
import VirtualList from "@/shared/virtual_list/virtual_list";
import "./paged_ticket_list.scss";

export const CONTROL_CLASS = "paged-ticket-list__control";

interface PagedTicketListProps {
  // The page's own class on the root, for its intro and row-content styles.
  className: string;
  // The page's usePagedCollection result; the list renders its states.
  list: PagedCollectionView;
  // The page's filter, drawn above the list.
  filter: ReactNode;
  intro?: ReactNode;
  filtered: boolean;
  // Shown instead of the list when the filter can match nothing at all.
  matchesNothingMessage?: string;
  loadingMessage: string;
  emptyMessage: { filtered: string; unfiltered: string };
  label: string;
  estimateRowHeight: number;
  renderRow: (ticket: Ticket) => ReactNode;
}

// PagedTicketList is the page both Blocked and Unreviewed are: a filter above
// a virtual list of tickets paged by the server, with the loading, empty and
// error states. Each page supplies only its query, its wording and its rows.
export default function PagedTicketList({
  className,
  list,
  filter,
  intro,
  filtered,
  matchesNothingMessage,
  loadingMessage,
  emptyMessage,
  label,
  estimateRowHeight,
  renderRow,
}: PagedTicketListProps) {
  const failed = list.error !== null;
  return (
    <div className={`paged-ticket-list ${className}`}>
      {intro}
      {filter}
      {matchesNothingMessage !== undefined ? (
        <p className="paged-ticket-list__empty">{matchesNothingMessage}</p>
      ) : !list.loaded && !failed ? (
        <p>{loadingMessage}</p>
      ) : list.items.length === 0 && !failed ? (
        <p className="paged-ticket-list__empty">{filtered ? emptyMessage.filtered : emptyMessage.unfiltered}</p>
      ) : (
        <>
          {list.items.length > 0 && (
            <VirtualList
              items={list.items}
              getKey={(ticket) => ticket.id}
              renderRow={renderRow}
              label={label}
              estimateRowHeight={estimateRowHeight}
              measureRows
              fill
              hasMore={list.hasMore}
              loading={list.loadingMore}
              onLoadMore={list.loadMore}
              mode="button"
            />
          )}
          {failed && (
            <div role="alert" className="paged-ticket-list__notice">
              {list.error}{" "}
              <Button className={CONTROL_CLASS} onClick={list.retry}>
                Try again
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

interface PagedTicketRowProps {
  ticketId: number;
  from: TabName;
  // The link's content: the ticket's number and title, styled by the page.
  link: ReactNode;
  // The row's other cells, after the link.
  children?: ReactNode;
  // A panel under the row, inside the same measured box.
  detail?: ReactNode;
}

export function PagedTicketRow({ ticketId, from, link, children, detail }: PagedTicketRowProps) {
  return (
    <div className="paged-ticket-list__slot">
      <div className="paged-ticket-list__row">
        <TicketLink ticketId={ticketId} from={from} className="paged-ticket-list__ticket">
          {link}
        </TicketLink>
        {children}
      </div>
      {detail}
    </div>
  );
}
