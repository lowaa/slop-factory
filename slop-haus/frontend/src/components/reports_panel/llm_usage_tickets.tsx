import { apiClient, type LlmUsageTicket, type LlmUsageTicketsQuery, type Priority } from "@/api";
import { formatCount } from "@/lib/format_usage";
import { TicketQueryModel, type TicketApiQuery } from "@/lib/ticket_query/ticket_query";
import { statusLabel } from "@/domain/board_columns";
import { TicketFilter } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import TicketLink from "@/shared/ticket_link/ticket_link";
import VirtualList from "@/shared/virtual_list/virtual_list";
import Cost from "./llm_usage_cost";
import { modeLabel } from "@/domain/llm_usage_labels";
import { reportFilterSearch, type ReportDays } from "./report_filter";
import { USAGE_TICKETS_FILTER_MODEL as MODEL } from "./usage_tickets_filter";
import { useUsageTickets } from "./use_usage_tickets";

export const USAGE_TICKETS_PAGE_SIZE = 50;
// A row is a title line and a line per billing mode.
const ESTIMATED_ROW_HEIGHT = 72;

const QUERY_MODEL = new TicketQueryModel(MODEL);

// The API's query for the filter, the report's period and priority, and a
// cursor. The list is ranked by tokens, so the sort the query model names is
// not sent.
function usageTicketsQuery(days: ReportDays, priority: Priority | undefined, includeUnreliable: boolean, api: TicketApiQuery): LlmUsageTicketsQuery {
  const { params } = api;
  const list = (value: string | readonly string[] | undefined) => (value === undefined ? undefined : typeof value === "string" ? [value] : [...value]);
  const text = typeof params.q === "string" ? params.q : undefined;
  return {
    days,
    ...(includeUnreliable && { include_unreliable: true }),
    status: api.status,
    ...(priority && { priority: [priority] }),
    ...(text && { q: text }),
    ...(params.created_from && { created_from: list(params.created_from)![0] }),
    ...(params.created_to && { created_to: list(params.created_to)![0] }),
    limit: USAGE_TICKETS_PAGE_SIZE,
  };
}

// What the page holds for the list: the report's priority, and the ticket
// filter (the period is passed on its own, as the rest of the section needs it).
export interface UsageTicketsFilterProps {
  // The report's priority. The list follows it rather than offering a second
  // priority control (see usage_tickets_filter.ts).
  priority?: Priority;
  values: FilterValues;
  onFilterChange: (values: FilterValues) => void;
  // Pins the list's window, which jsdom cannot lay out.
  viewportHeight?: number;
}

// UsageTickets is the tickets with the most usage, filtered with the shared
// ticket filter and paged by the server: a busy period has hundreds, and the
// biggest are the ones worth a look.
export default function UsageTickets({ days, includeUnreliable = false, priority, values, onFilterChange, viewportHeight }: UsageTicketsFilterProps & { days: ReportDays; includeUnreliable?: boolean }) {
  const query = QUERY_MODEL.toApi(QUERY_MODEL.initial(values));
  // The list starts again for a different filter, period or priority, with
  // its own paging state, so a reply to the previous one cannot land in it.
  const listKey = JSON.stringify([days, includeUnreliable, priority ?? "", QUERY_MODEL.key(QUERY_MODEL.initial(values))]);
  // What the Back link from a ticket returns to: the report's own filter
  // with this one.
  const reportSearch = { ...reportFilterSearch({ days, priority }) };
  return (
    <div className="llm-usage__tickets">
      <TicketFilter
        legend="Ticket filters"
        model={MODEL}
        values={values}
        onChange={onFilterChange}
        extraSearch={reportSearch}
      />
      <TicketList key={listKey} days={days} includeUnreliable={includeUnreliable} priority={priority} query={query} filtered={MODEL.isFiltered(values) || priority !== undefined} viewportHeight={viewportHeight} />
    </div>
  );
}

interface TicketListProps {
  days: ReportDays;
  includeUnreliable: boolean;
  priority?: Priority;
  query: TicketApiQuery;
  filtered: boolean;
  viewportHeight?: number;
}

function TicketList({ days, includeUnreliable, priority, query, filtered, viewportHeight }: TicketListProps) {
  const request = usageTicketsQuery(days, priority, includeUnreliable, query);
  const list = useUsageTickets(
    (cursor) => apiClient.getLlmUsageTickets({ ...request, ...(cursor && { cursor }) }),
    !query.matchesNothing,
  );
  const { items, total, loaded, loading, error } = list;
  return (
    <>
      {items.length > 0 && (
        <VirtualList
          items={items}
          getKey={(ticket) => ticket.id}
          renderRow={(ticket) => <UsageTicketRow ticket={ticket} />}
          label="LLM usage by ticket"
          estimateRowHeight={ESTIMATED_ROW_HEIGHT}
          measureRows
          totalCount={total}
          hasMore={list.hasMore}
          loading={loading}
          onLoadMore={list.loadMore}
          viewportHeight={viewportHeight}
        />
      )}
      {loading && <p className="llm-usage__note">Loading tickets…</p>}
      {loaded && !loading && !error && items.length === 0 && (
        <p className="llm-usage__note">
          {filtered ? "No ticket with usage matches these filters." : "No ticket has usage in this period."}
        </p>
      )}
      {error && (
        <p role="alert" className="llm-usage__error">
          {error}
        </p>
      )}
      {!loading && items.length > 0 && (
        <p className="llm-usage__note">
          Showing {items.length} of {total}, most tokens first.
        </p>
      )}
      {error && (
        <button type="button" className="llm-usage__more" onClick={list.retry}>
          Try again
        </button>
      )}
    </>
  );
}

// A ticket's worker usage and the guidance declared for it are separate
// figures, never one sum: the worker is the agent that did the ticket, the
// guidance is a console or Remote Control session that steered it.
const KINDS = [
  { key: "modes", label: "worker" },
  { key: "guidance_modes", label: "guidance" },
] as const;

function UsageTicketRow({ ticket }: { ticket: LlmUsageTicket }) {
  return (
    <div className="llm-usage__ticket">
      <TicketLink ticketId={ticket.id} from="reports" className="llm-usage__ticket-link">
        <span className="llm-usage__ticket-id">#{ticket.id}</span> {ticket.title}
      </TicketLink>
      <span className="llm-usage__ticket-status">{statusLabel(ticket.status)}</span>
      <span className="llm-usage__ticket-modes">
        {KINDS.flatMap(({ key, label }) =>
          ticket[key].map((m) => (
            <span key={`${key}-${m.billing_mode}`} className="llm-usage__ticket-mode" data-kind={label}>
              <span className="llm-usage__ticket-kind">{label}</span>{" "}
              <span className="llm-usage__mode-name">{modeLabel(m.billing_mode)}</span>{" "}
              {formatCount(m.input_tokens + m.output_tokens + m.cache_read_tokens + m.cache_write_tokens)} tokens,{" "}
              <Cost sums={m} />
            </span>
          )),
        )}
      </span>
    </div>
  );
}
