import { useEffect, useRef } from "react";
import { type Ticket } from "@/api";
import { liveEvents } from "./live_events";

// How often a view refetches at most. The stream itself sends at most one
// change a second; this also bounds fast tab switching.
export const LIVE_REFRESH_THROTTLE_MS = 2000;

// useLiveRefresh keeps a mounted view current without a page reload, by
// calling refresh -- throttled, leading and trailing, so a burst of changes
// costs two fetches rather than one each -- when the board stream says
// something changed, or when events may have been missed (the tab became
// visible again, or the page was restored from the back/forward cache).
// The connection itself is shared: see LiveEvents.
//
// The view's own mount fetch counts as the first of the throttle's window.
// The stream's first event after mount -- the revision as it stands -- is
// refetched for too, a window later: the mount fetch and the stream's first
// read race, and a change landing between them would otherwise wait for the
// next one.
//
// A stream event that names the tickets that changed hands them to
// onTickets at once (with the attempt cards the event carries for them), to
// apply in place, and asks only for a refresh without the columns
// (columns: false) -- the panels that are not tickets still refetch. An
// event that cannot (a reload, or a server that predates the tickets) asks
// for the columns too.
export interface LiveRefreshScope {
  columns: boolean;
}

export function useLiveRefresh(
  refresh: (scope: LiveRefreshScope) => void,
  throttleMs = LIVE_REFRESH_THROTTLE_MS,
  onTickets?: (tickets: Ticket[], cards: Ticket[]) => void,
): void {
  const refreshRef = useRef(refresh);
  const onTicketsRef = useRef(onTickets);
  useEffect(() => {
    refreshRef.current = refresh;
    onTicketsRef.current = onTickets;
  }, [refresh, onTickets]);

  useEffect(() => {
    let lastRun = Date.now();
    let pending: ReturnType<typeof setTimeout> | undefined;
    // Columns are wanted if any request folded into the next run wants them.
    let columnsWanted = false;
    const run = () => {
      pending = undefined;
      lastRun = Date.now();
      const columns = columnsWanted;
      columnsWanted = false;
      refreshRef.current({ columns });
    };
    const request = (columns = true) => {
      columnsWanted ||= columns;
      if (pending !== undefined) return;
      const wait = lastRun + throttleMs - Date.now();
      if (wait <= 0) run();
      else pending = setTimeout(run, wait);
    };
    // A refetch still waiting when the page goes away would run hidden, or
    // -- frozen in the back/forward cache -- hold back the refetch the
    // restore asks for. Coming back requests one anyway.
    const cancelPending = () => {
      clearTimeout(pending);
      pending = undefined;
    };

    const unsubscribe = liveEvents.subscribe("board", (event) => {
      if (event.kind === "pause") return cancelPending();
      if (event.kind === "resync") return request();
      const tickets = event.change.tickets;
      if (!event.change.reload && tickets?.length && onTicketsRef.current) {
        onTicketsRef.current(tickets, event.change.cards ?? []);
        request(false);
      } else {
        request();
      }
    });
    return () => {
      unsubscribe();
      cancelPending();
    };
  }, [throttleMs]);
}
