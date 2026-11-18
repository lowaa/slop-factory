import { useEffect, useState } from "react";
import { apiClient, type Priority, type ReportWaitingTicket, type Status } from "@/api";
import { statusLabel } from "@/domain/board_columns";
import TicketLink from "@/shared/ticket_link/ticket_link";
import Timestamp from "@/shared/timestamp/timestamp";
import "./where_now.scss";
import { errorMessage } from "@/lib/use_async_action";

export const WAITING_PAGE_SIZE = 10;

type WaitingStatus = NonNullable<Parameters<typeof apiClient.getReportWaiting>[0]["status"]>;

interface WaitingListProps {
  status: WaitingStatus;
  priority?: Priority;
  includeParked?: boolean;
}

type WaitingState =
  | { phase: "loading"; tickets: ReportWaitingTicket[]; total: number }
  | { phase: "error"; tickets: ReportWaitingTicket[]; total: number; message: string }
  | { phase: "ready"; tickets: ReportWaitingTicket[]; total: number };

// WaitingList is the tickets that have been in one status longest, a page
// at a time: a status can hold hundreds, and the longest waits are the ones
// worth looking at.
function WaitingList({ status, priority, includeParked }: WaitingListProps) {
  const [state, setState] = useState<WaitingState>({ phase: "loading", tickets: [], total: 0 });
  // The page to fetch. Changing it is what loads one, so the first page,
  // "Show more" and "Try again" are the same effect; attempt is what makes a
  // retry of the same offset a change.
  const [request, setRequest] = useState({ offset: 0, attempt: 0 });

  useEffect(() => {
    let cancelled = false;
    apiClient
      .getReportWaiting({ status, priority, include_parked: includeParked, limit: WAITING_PAGE_SIZE, offset: request.offset })
      .then((page) => {
        if (cancelled) return;
        // The list moves under offset paging as tickets enter and leave the
        // status, so a ticket on the previous page can come round again.
        setState((prev) => {
          const seen = new Set(prev.tickets.map((t) => t.id));
          return {
            phase: "ready",
            tickets: [...prev.tickets, ...page.tickets.filter((t) => !seen.has(t.id))],
            total: page.total,
          };
        });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = errorMessage(err, "Failed to load tickets.");
        setState((prev) => ({ phase: "error", tickets: prev.tickets, total: prev.total, message }));
      });
    return () => {
      cancelled = true;
    };
  }, [status, priority, includeParked, request]);

  const loadNext = () => {
    setState((prev) => ({ ...prev, phase: "loading" }));
    setRequest((prev) => ({ offset: state.tickets.length, attempt: prev.attempt + 1 }));
  };

  return (
    <div className="where-now__waiting">
      {state.tickets.length > 0 && (
        <ul className="where-now__tickets">
          {state.tickets.map((ticket) => (
            <li key={ticket.id} className="where-now__ticket">
              <TicketLink ticketId={ticket.id} from="reports" className="where-now__link">
                <span className="where-now__id">#{ticket.id}</span> {ticket.title}
              </TicketLink>
              <span className="where-now__since">
                since <Timestamp at={ticket.since} />
              </span>
            </li>
          ))}
        </ul>
      )}
      {state.phase === "loading" && <p className="where-now__note">Loading tickets…</p>}
      {state.phase === "ready" && state.tickets.length === 0 && (
        <p className="where-now__note">No ticket here has a recorded time.</p>
      )}
      {state.phase === "error" && (
        <p role="alert" className="where-now__error">
          {state.message}
        </p>
      )}
      {state.phase !== "loading" && state.tickets.length > 0 && (
        <p className="where-now__note">
          Showing {state.tickets.length} of {state.total}, longest waiting first.
        </p>
      )}
      {state.phase === "ready" && state.tickets.length < state.total && (
        <button type="button" className="where-now__more" onClick={loadNext}>
          Show more
        </button>
      )}
      {state.phase === "error" && (
        <button type="button" className="where-now__more" onClick={loadNext}>
          Try again
        </button>
      )}
    </div>
  );
}

interface WhereNowProps {
  rows: { status: Status; count: number; without_history: number }[];
  priority?: Priority;
  includeParked?: boolean;
  // Tickets parked now that the server left out of rows.
  parkedNotShown?: number;
}

const BOARD_ORDER: string[] = ["parked", "preparing", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review"];

// WhereNow counts the tickets in each unfinished status as they are now,
// each opening to its longest-waiting tickets.
export default function WhereNow({ rows, priority, includeParked, parkedNotShown = 0 }: WhereNowProps) {
  const [openStatus, setOpenStatus] = useState<string | null>(null);
  const ordered = [...rows].sort((a, b) => BOARD_ORDER.indexOf(a.status) - BOARD_ORDER.indexOf(b.status));
  const withoutHistory = rows.reduce((sum, r) => sum + r.without_history, 0);

  return (
    <div className="where-now">
      {ordered.length === 0 && <p className="where-now__note">No tickets are waiting.</p>}
      <ul className="where-now__statuses">
        {ordered.map((row) => {
          const label = statusLabel(row.status);
          const open = openStatus === row.status;
          return (
            <li key={row.status} className="where-now__status">
              <button
                type="button"
                className="where-now__toggle"
                aria-expanded={open}
                onClick={() => setOpenStatus(open ? null : row.status)}
              >
                <span className="where-now__label">{label}</span>
                <span className="where-now__count">{row.count}</span>
              </button>
              {open && <WaitingList key={`${row.status}/${priority ?? ""}`} status={row.status as WaitingStatus} priority={priority} includeParked={includeParked} />}
            </li>
          );
        })}
      </ul>
      {parkedNotShown > 0 && (
        <p className="where-now__note" data-testid="parked-not-shown">
          {parkedNotShown} parked not shown.
        </p>
      )}
      {withoutHistory > 0 && (
        <p className="where-now__note">
          {withoutHistory} {withoutHistory === 1 ? "has" : "have"} no recorded time (last moved before history was
          kept).
        </p>
      )}
    </div>
  );
}
