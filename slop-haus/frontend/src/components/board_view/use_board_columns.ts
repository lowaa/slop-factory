import { useMemo } from "react";
import type { Status } from "@/api";
import { useLogger } from "@/context/logger";
import { usePagedCollection, type PagedCollectionView } from "@/lib/paged_collection/use_paged_collection";
import { BOARD_STATUSES } from "@/domain/board_columns";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import type { FilterValues } from "@/domain/ticket_filter_model";
import type { SubscribeChanges } from "@/lib/paged_collection/paged_collection";
import { BOARD_COLUMN_QUERY_MODELS, fetchBoardPage } from "./board_query";

export type BoardColumns = Record<Status, PagedCollectionView>;

// Every column's list, keyed "board:<status>" so no two share state. The
// column set is a constant, so the hooks run in the same order every render.
// `subscribe` is where live ticket changes come from; each column applies
// them in place.
export function useBoardColumns(values: FilterValues, sort: SortId, subscribe?: SubscribeChanges): BoardColumns {
  const logger = useLogger();
  const columns = {} as BoardColumns;
  for (const status of BOARD_STATUSES) {
    const queryModel = BOARD_COLUMN_QUERY_MODELS[status];
    // eslint-disable-next-line react-hooks/rules-of-hooks
    const query = useMemo(() => queryModel.initial(values, sort), [queryModel, values, sort]);
    // eslint-disable-next-line react-hooks/rules-of-hooks
    const deps = useMemo(() => ({ key: `board:${status}`, queryModel, fetchPage: fetchBoardPage, logger, subscribe }), [status, queryModel, logger, subscribe]);
    // eslint-disable-next-line react-hooks/rules-of-hooks
    columns[status] = usePagedCollection(deps, query);
  }
  return columns;
}
