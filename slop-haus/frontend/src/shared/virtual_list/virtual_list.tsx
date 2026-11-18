import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { FocusEvent, ReactNode } from "react";
import { defaultRangeExtractor, useVirtualizer } from "@tanstack/react-virtual";
import type { Range } from "@tanstack/react-virtual";
import { Button } from "@slop-factory/ui";
import "./virtual_list.scss";

// The only module that imports @tanstack/react-virtual: the rest of the app
// gets a list that draws just the rows on screen while still telling
// assistive technology how many rows there really are and where each sits.

export type LoadMoreMode = "infinite" | "button";

export interface VirtualListProps<T> {
  items: readonly T[];
  getKey: (item: T, index: number) => string | number;
  renderRow: (item: T, index: number) => ReactNode;
  // Names the list for screen readers. Several lists on a page are
  // otherwise indistinguishable.
  label: string;
  estimateRowHeight: number;
  // Rows whose height depends on their content are measured after they
  // render; estimateRowHeight is then only the guess for rows not yet seen.
  measureRows?: boolean;
  // The size of the full result when it is known and larger than items.
  // Without it the set size is items.length, or unknown (-1) while hasMore.
  totalCount?: number;
  hasMore?: boolean;
  loading?: boolean;
  onLoadMore?: () => void;
  mode?: LoadMoreMode;
  // How many rows from the end of the loaded items trigger an
  // infinite-scroll load.
  loadAhead?: number;
  overscan?: number;
  // Pins the viewport to this many pixels instead of the CSS size, and stops
  // it being observed. jsdom has no layout, so tests (and stories that need
  // a known window) set it.
  viewportHeight?: number;
  // Takes the height the page's layout leaves instead of a fixed 480px. Only
  // works when every ancestor up to a bounded one is a flex column with
  // flex: 1 and min-height: 0; otherwise the list grows to its full height
  // and nothing is virtual.
  fill?: boolean;
}

const DEFAULT_LOAD_AHEAD = 5;
const DEFAULT_OVERSCAN = 4;

function setSize(totalCount: number | undefined, itemCount: number, hasMore: boolean): number {
  if (totalCount !== undefined) return Math.max(totalCount, itemCount);
  // aria-setsize of -1 is how ARIA says "more than are loaded, count unknown".
  return hasMore ? -1 : itemCount;
}

export default function VirtualList<T>({
  items,
  getKey,
  renderRow,
  label,
  estimateRowHeight,
  measureRows = false,
  totalCount,
  hasMore = false,
  loading = false,
  onLoadMore,
  mode = "infinite",
  loadAhead = DEFAULT_LOAD_AHEAD,
  overscan = DEFAULT_OVERSCAN,
  viewportHeight,
  fill = false,
}: VirtualListProps<T>) {
  const scrollRef = useRef<HTMLDivElement>(null);
  // Held by key, not index: a row inserted above the focused one (a live
  // event, a prepended page) moves it, and the retained slot must follow.
  const [focusedKey, setFocusedKey] = useState<string | number | null>(null);
  const focusedIndex = useMemo(() => {
    if (focusedKey === null) return null;
    const index = items.findIndex((item, i) => getKey(item, i) === focusedKey);
    return index === -1 ? null : index;
  }, [focusedKey, items, getKey]);

  // A row that holds focus stays rendered however far it is scrolled, so the
  // browser keeps focus on it and scrolling back finds it where it was.
  // Unmounting it would drop focus to <body>, and a keyboard user would
  // start again from the top of the page.
  const rangeExtractor = useCallback(
    (range: Range) => {
      const indexes = defaultRangeExtractor(range);
      if (focusedIndex === null || focusedIndex >= range.count || indexes.includes(focusedIndex)) return indexes;
      return [...indexes, focusedIndex].sort((a, b) => a - b);
    },
    [focusedIndex],
  );

  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => estimateRowHeight,
    overscan,
    rangeExtractor,
    getItemKey: (index) => getKey(items[index], index),
    ...(viewportHeight === undefined
      ? {}
      : {
          initialRect: { width: 0, height: viewportHeight },
          observeElementRect: (_instance, cb) => cb({ width: 0, height: viewportHeight }),
        }),
  });

  const virtualItems = virtualizer.getVirtualItems();
  const size = setSize(totalCount, items.length, hasMore);

  // The latest rendered row *by position*: the focused row kept alive above
  // can be far from the end, but the range's end is what says the user is
  // near it.
  const lastIndex = virtualizer.range?.endIndex ?? -1;

  // Infinite scroll asks once per loaded set, identified by its length and
  // end keys so a refreshed list of the same length still asks. A failed
  // load leaves hasMore true and loading false with the same set, and asking
  // again on every render would hammer a failing endpoint; scrolling away
  // from the end and back, the button mode, or the caller's own retry ask
  // again.
  const requestedFor = useRef<string | null>(null);
  const loadedSet =
    items.length === 0
      ? "empty"
      : `${items.length}:${getKey(items[0], 0)}:${getKey(items[items.length - 1], items.length - 1)}`;
  const onLoadMoreRef = useRef(onLoadMore);
  useLayoutEffect(() => {
    onLoadMoreRef.current = onLoadMore;
  });
  useEffect(() => {
    if (mode !== "infinite" || !hasMore || loading) return;
    if (lastIndex < items.length - 1 - loadAhead) {
      // Scrolling away from the end re-arms the request, so a user can retry
      // a failed load by scrolling back down.
      requestedFor.current = null;
      return;
    }
    if (requestedFor.current === loadedSet) return;
    requestedFor.current = loadedSet;
    onLoadMoreRef.current?.();
  }, [mode, hasMore, loading, lastIndex, items.length, loadAhead, loadedSet]);

  const announcement = useLoadedAnnouncement(items.length, size);

  const handleFocus = (key: string | number) => () => setFocusedKey(key);
  const handleBlur = (event: FocusEvent<HTMLUListElement>) => {
    if (!event.currentTarget.contains(event.relatedTarget)) setFocusedKey(null);
  };

  return (
    <div className={fill ? "virtual-list virtual-list--fill" : "virtual-list"}>
      {/* Focusable so the keyboard can scroll it (arrows, Page Up/Down,
          Home, End) even when no row holds a control. */}
      <div
        ref={scrollRef}
        className="virtual-list__viewport"
        style={viewportHeight === undefined ? undefined : { height: viewportHeight }}
        role="region"
        aria-label={label}
        tabIndex={0}
      >
        <ul
          role="list"
          className="virtual-list__rows"
          style={{ height: virtualizer.getTotalSize() }}
          aria-busy={loading}
          onBlur={handleBlur}
        >
          {virtualItems.map((row) => (
            <li
              key={row.key}
              ref={measureRows ? virtualizer.measureElement : undefined}
              data-index={row.index}
              role="listitem"
              aria-setsize={size}
              aria-posinset={row.index + 1}
              className="virtual-list__row"
              style={{
                transform: `translateY(${row.start}px)`,
                ...(measureRows ? undefined : { height: row.size }),
              }}
              onFocus={handleFocus(row.key as string | number)}
            >
              {renderRow(items[row.index], row.index)}
            </li>
          ))}
        </ul>
      </div>
      {mode === "button" && hasMore && (
        <div className="virtual-list__footer">
          <Button onClick={onLoadMore} disabled={loading}>
            {loading ? "Loading..." : "Load more"}
          </Button>
        </div>
      )}
      <div className="virtual-list__status" role="status" aria-live="polite">
        {announcement}
      </div>
    </div>
  );
}

// Rows arriving below the fold are invisible to a screen reader user, so
// say so. The first render and a shrinking list (a new filter) are not
// "more rows loading" and stay silent.
function useLoadedAnnouncement(itemCount: number, size: number): string {
  const previous = useRef(itemCount);
  const [message, setMessage] = useState("");
  useEffect(() => {
    const added = itemCount - previous.current;
    previous.current = itemCount;
    if (added <= 0) {
      setMessage("");
      return;
    }
    const of = size > 0 ? ` of ${size}` : "";
    setMessage(`${added} more ${added === 1 ? "row" : "rows"} loaded, ${itemCount}${of} shown`);
  }, [itemCount, size]);
  return message;
}
