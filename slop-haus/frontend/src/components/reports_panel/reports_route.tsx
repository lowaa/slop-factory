import { useCallback, useMemo } from "react";
import { getRouteApi } from "@slop-factory/router";
import { useRouteActive } from "@/shared/route_active/use_route_active";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { reportFilterFromSearch, reportFilterSearch, type ReportFilter } from "./report_filter";
import ReportsPanel from "./reports_panel";
import { USAGE_TICKETS_FILTER_MODEL } from "./usage_tickets_filter";

// By id rather than importing the route from routes.tsx, which imports
// this feature.
const reportsRouteApi = getRouteApi("/signed-in/reports");

// ReportsRoute keeps the period and priority in the URL, so a report can be
// reloaded or sent as a link. Changing one replaces the entry: Back leaves
// the tab rather than stepping through every period tried.
export default function ReportsRoute() {
  const { days, priority, include_parked, ...ticketSearch } = reportsRouteApi.useSearch();
  const navigate = reportsRouteApi.useNavigate();
  const isActive = useRouteActive(reportsRouteApi.id);
  const filter = useMemo(
    () => reportFilterFromSearch({ days, priority, include_parked }),
    [days, priority, include_parked],
  );
  const ticketSearchKey = JSON.stringify(ticketSearch);
  const ticketValues = useMemo(
    () => USAGE_TICKETS_FILTER_MODEL.fromSearch(JSON.parse(ticketSearchKey) as Record<string, unknown>),
    [ticketSearchKey],
  );
  // The period and priority leave the by-ticket filter as it is, and the
  // other way round: each is its own part of the address.
  const onChange = useCallback(
    (next: ReportFilter) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({
          ...prev,
          days: undefined,
          priority: undefined,
          include_parked: undefined,
          ...reportFilterSearch(next),
        }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  const onTicketFilterChange = useCallback(
    (next: FilterValues) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...USAGE_TICKETS_FILTER_MODEL.searchPatch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  return (
    <ReportsPanel
      filter={filter}
      onChange={onChange}
      ticketValues={ticketValues}
      onTicketFilterChange={onTicketFilterChange}
    />
  );
}
