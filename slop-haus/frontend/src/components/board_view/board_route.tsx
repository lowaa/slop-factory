import { useCallback, useEffect, useMemo } from "react";
import { getRouteApi } from "@slop-factory/router";
import { useRouteActive } from "@/shared/route_active/use_route_active";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import { rememberBoardColumn, rememberedBoardColumn } from "@/domain/board_columns";
import { BOARD_FILTER_MODEL, BOARD_QUERY_MODEL } from "@/domain/board_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import BoardView from "./board_view";

const boardRouteApi = getRouteApi("/signed-in/");

// BoardRoute keeps the phone board's column in the URL (?column=), so a
// reload keeps it. Choosing one replaces the history entry rather than
// adding one: Back leaves the board, not steps back through the chips.
//
// The filters (?done=, ?parked=, ?q=) are in the URL too, so a filtered board can be
// reloaded or sent as a link, and replace the entry for the same reason:
// every pause in typing is a change.
export default function BoardRoute() {
  const { column, ...search } = boardRouteApi.useSearch();
  const navigate = boardRouteApi.useNavigate();
  const isActive = useRouteActive(boardRouteApi.id);
  const searchKey = JSON.stringify(search);
  const filterValues = useMemo(() => BOARD_FILTER_MODEL.fromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onFilterChange = useCallback(
    (next: FilterValues) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...BOARD_FILTER_MODEL.searchPatch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );

  const sort = useMemo(() => BOARD_QUERY_MODEL.sortFromSearch(JSON.parse(searchKey) as Record<string, unknown>), [searchKey]);
  const onSortChange = useCallback(
    (next: SortId) => {
      if (!isActive()) return;
      void navigate({
        search: (prev) => ({ ...prev, ...BOARD_QUERY_MODEL.sortModel.toSearch(next) }),
        replace: true,
      });
    },
    [isActive, navigate],
  );

  // A column the address names is remembered too, not only one chosen
  // here: a board opened at ?column=blocked comes back to Blocked from a
  // ticket's Back link like one where Blocked was picked.
  useEffect(() => {
    if (column) rememberBoardColumn(column);
  }, [column]);
  return (
    <BoardView
      filterValues={filterValues}
      onFilterChange={onFilterChange}
      sort={sort}
      onSortChange={onSortChange}
      column={column ?? rememberedBoardColumn()}
      onColumnChange={(next) =>
        void navigate({
          search: (prev) => ({ ...prev, column: next }),
          replace: true,
        })
      }
    />
  );
}
