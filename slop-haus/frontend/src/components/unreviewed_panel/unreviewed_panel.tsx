import { useMemo } from "react";
import type { Ticket } from "@/api";
import { useLogger } from "@/context/logger";
import { usePagedCollection } from "@/lib/paged_collection/use_paged_collection";
import PrLink from "@/shared/pr_link/pr_link";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import { TicketFilter } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import Timestamp from "@/shared/timestamp/timestamp";
import { PagedTicketList, PagedTicketRow } from "@/components/paged_ticket_list";

import { UNREVIEWED_FILTER_MODEL as MODEL, UNREVIEWED_QUERY_MODEL, UNREVIEWED_SORT_MODEL, fetchUnreviewedPage } from "@/domain/unreviewed_filter";
import "./unreviewed_panel.scss";

interface UnreviewedPanelProps {
  values: FilterValues;
  // A changed filter replaces the entry and starts the list again from the top.
  onFilterChange: (values: FilterValues) => void;
  sort: SortId;
  // A changed sort starts the list again from the top, keeping the filter.
  onSortChange: (sort: SortId) => void;
}

const ROW_HEIGHT = 52;

// UnreviewedPanel lists the done tickets whose change merged without a
// person approving it first and that nobody has marked reviewed since, most
// recently merged first unless another sort is picked -- the server's filter, order and paging, not this
// component's. Each one is marked reviewed from its own page, where the
// ticket's history and a follow-up form are to hand.
export default function UnreviewedPanel({ values, onFilterChange, sort, onSortChange }: UnreviewedPanelProps) {
  const logger = useLogger();
  const query = useMemo(() => UNREVIEWED_QUERY_MODEL.initial(values, sort), [values, sort]);
  const deps = useMemo(
    () => ({ key: "unreviewed", queryModel: UNREVIEWED_QUERY_MODEL, fetchPage: fetchUnreviewedPage, logger }),
    [logger],
  );
  const list = usePagedCollection(deps, query);

  return (
    <PagedTicketList
      className="unreviewed-panel"
      list={list}
      intro={
        <p className="unreviewed-panel__intro">
          Finished tickets whose PR merged without anyone approving it first. Open one to look over
          its change and mark it reviewed.
        </p>
      }
      filter={
        <TicketFilter
          model={MODEL} values={values}
          onChange={onFilterChange}
          sort={{ model: UNREVIEWED_SORT_MODEL, value: sort, onChange: onSortChange }}
        />
      }
      filtered={MODEL.isFiltered(values)}
      loadingMessage="Loading tickets…"
      emptyMessage={{
        filtered: "No tickets match these filters.",
        unfiltered: "Nothing to review -- every merged change has been looked at.",
      }}
      label="Tickets awaiting review"
      estimateRowHeight={ROW_HEIGHT}
      renderRow={(ticket) => <UnreviewedRow ticket={ticket} />}
    />
  );
}

function UnreviewedRow({ ticket }: { ticket: Ticket }) {
  return (
    <PagedTicketRow
      ticketId={ticket.id}
      from="unreviewed"
      link={
        <>
          <span className="unreviewed-panel__id">#{ticket.id}</span> {ticket.title}
        </>
      }
    >
      {ticket.closed_at && (
        <span className="unreviewed-panel__closed">
          Merged <Timestamp at={ticket.closed_at} />
        </span>
      )}
      {ticket.pr_url ? (
        <PrLink url={ticket.pr_url} className="unreviewed-panel__pr" ariaLabel={`PR for #${ticket.id}`}>
          PR
        </PrLink>
      ) : (
        <span className="unreviewed-panel__no-pr">no PR reported</span>
      )}
    </PagedTicketRow>
  );
}
