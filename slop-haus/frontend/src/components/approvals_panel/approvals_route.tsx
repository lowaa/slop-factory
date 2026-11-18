import { useCallback, useMemo } from "react";
import { getRouteApi } from "@slop-factory/router";
import { useRouteActive } from "@/shared/route_active/use_route_active";
import type { FilterValues } from "@/domain/ticket_filter_model";
import { APPROVALS_FILTER_MODEL } from "@/domain/approvals_filter";
import ApprovalsPanel from "./approvals_panel";

const approvalsRouteApi = getRouteApi("/signed-in/approvals");

// ApprovalsRoute keeps the filter in the URL, as Blocked does, so a Back link
// from a ticket returns to the filtered proposals.
export default function ApprovalsRoute() {
  const search = approvalsRouteApi.useSearch();
  const navigate = approvalsRouteApi.useNavigate();
  const isActive = useRouteActive(approvalsRouteApi.id);
  const searchKey = JSON.stringify(search);
  const values = useMemo(() => APPROVALS_FILTER_MODEL.fromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onFilterChange = useCallback(
    (next: FilterValues) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...APPROVALS_FILTER_MODEL.searchPatch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );
  return <ApprovalsPanel values={values} onFilterChange={onFilterChange} />;
}
