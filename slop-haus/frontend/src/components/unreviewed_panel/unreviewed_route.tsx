import { useCallback, useMemo } from "react";
import { getRouteApi } from "@slop-factory/router";
import { useRouteActive } from "@/shared/route_active/use_route_active";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { UNREVIEWED_FILTER_MODEL, UNREVIEWED_QUERY_MODEL } from "@/domain/unreviewed_filter";
import UnreviewedPanel from "./unreviewed_panel";

const unreviewedRouteApi = getRouteApi("/signed-in/unreviewed");

// UnreviewedRoute keeps the filter in the URL, so a filtered view can be sent
// as a link and Back from a ticket returns to it. Changing the filter
// replaces the entry, as on the board. How far the list has been paged is
// not in it: with "load more" a cursor would not describe what is on
// screen, so a reload starts at the top.
export default function UnreviewedRoute() {
  const search = unreviewedRouteApi.useSearch();
  const navigate = unreviewedRouteApi.useNavigate();
  const isActive = useRouteActive(unreviewedRouteApi.id);
  const searchKey = JSON.stringify(search);
  const values = useMemo(() => UNREVIEWED_FILTER_MODEL.fromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onFilterChange = useCallback(
    (next: FilterValues) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...UNREVIEWED_FILTER_MODEL.searchPatch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  const sort = useMemo(() => UNREVIEWED_QUERY_MODEL.sortFromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onSortChange = useCallback(
    (next: SortId) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...UNREVIEWED_QUERY_MODEL.sortModel.toSearch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  return <UnreviewedPanel values={values} onFilterChange={onFilterChange} sort={sort} onSortChange={onSortChange} />;
}
