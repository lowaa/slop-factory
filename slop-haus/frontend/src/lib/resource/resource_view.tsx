import type { ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import type { Resource } from "./use_resource";

interface ResourceViewProps<T> {
  // A useResource or usePolledResource result.
  state: Pick<Resource<T>, "status" | "data" | "error"> & { reload?: () => Promise<void> };
  // Shown while the first load is in flight.
  loading?: string;
  // Returns true when the data holds nothing to list; the empty node is then
  // shown instead of children.
  isEmpty?: (data: T) => boolean;
  empty?: ReactNode;
  // Class of the loading and error text, so a panel keeps its own styling.
  noticeClassName?: string;
  children: (data: T) => ReactNode;
}

// ResourceView is the one loading / error-with-retry / empty contract for a
// view over a resource. The retry button sits beside the alert, not inside
// it, so the alert reads as the message alone.
export function ResourceView<T>({ state, loading = "Loading…", isEmpty, empty, noticeClassName, children }: ResourceViewProps<T>) {
  if (state.status === "loading") {
    return (
      <p role="status" className={noticeClassName}>
        {loading}
      </p>
    );
  }
  if (state.status === "error") {
    return (
      <>
        <p role="alert" className={noticeClassName}>
          {state.error}
        </p>
        {state.reload && (
          <Button size="sm" onClick={() => void state.reload?.()}>
            Try again
          </Button>
        )}
      </>
    );
  }
  if (state.data === undefined) return null;
  if (isEmpty?.(state.data)) return <>{empty ?? null}</>;
  return <>{children(state.data)}</>;
}
