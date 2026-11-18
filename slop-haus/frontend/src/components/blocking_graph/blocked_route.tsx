import { useCallback, useMemo } from "react";
import { getRouteApi } from "@slop-factory/router";
import { useRouteActive } from "@/shared/route_active/use_route_active";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { BLOCKED_FILTER_MODEL } from "./blocked_filter";
import BlockingGraphView from "./blocking_graph";

const blockedRouteApi = getRouteApi("/signed-in/blocked");

// BlockedRoute keeps the filter in the URL, as Unreviewed does, so a Back link
// from a ticket returns to the filtered graph. Changing it replaces the entry.
export default function BlockedRoute() {
  const search = blockedRouteApi.useSearch();
  const navigate = blockedRouteApi.useNavigate();
  const isActive = useRouteActive(blockedRouteApi.id);
  const searchKey = JSON.stringify(search);
  const values = useMemo(() => BLOCKED_FILTER_MODEL.fromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onFilterChange = useCallback(
    (next: FilterValues) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...BLOCKED_FILTER_MODEL.searchPatch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  return <BlockingGraphView values={values} onFilterChange={onFilterChange} />;
}
