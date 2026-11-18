import { defaultResourceClient, type ResourceClient } from "./client";
import type { ResourceKey } from "./keys";

// Writes a known result into the cache at once. A read still in flight is
// cancelled first: it began before this write, so landing after it would put
// the older value back. Unlike setResource, the write is synchronous, so a
// caller can read-modify-write without another write interleaving.
export function updateResource<T>(key: ResourceKey, updater: (current: T | undefined) => T, client: ResourceClient = defaultResourceClient): void {
  const current = client.getQueryData<T>(key);
  const next = updater(current);
  // A write that changes nothing has no reason to cancel a read. Exact, since
  // keys share prefixes (a ticket's comments sit under the ticket's key) and
  // a write to one is not news to the others.
  if (next === current) return;
  void client.cancelQueries({ queryKey: key, exact: true }, { revert: true });
  client.setQueryData<T>(key, next);
}

export interface Mutation<T, R> {
  // Shown while the request is in flight.
  optimistic?: (current: T) => T;
  request: () => Promise<R>;
  // Folds the reply into whatever the key holds by then. It gets the current
  // value rather than the one at the start, since other writes may have landed.
  commit: (current: T, result: R) => T;
  // Takes the optimistic change back after a failed request. It gets the
  // current value and the one before the optimistic change, so it can undo
  // only what this mutation did and leave later writes alone. Defaults to
  // restoring the earlier value whole.
  rollback?: (current: T, previous: T) => T;
}

// mutateResource runs a write with an optimistic update over a cached value:
// the change shows at once, the reply replaces it, and a failure undoes it
// before the error is thrown to the caller. Does nothing optimistic while the
// key holds no value.
export async function mutateResource<T, R>(key: ResourceKey, mutation: Mutation<T, R>, client: ResourceClient = defaultResourceClient): Promise<R> {
  const previous = client.getQueryData<T>(key);
  if (previous !== undefined && mutation.optimistic) {
    const { optimistic } = mutation;
    updateResource<T>(key, (current) => optimistic(current as T), client);
  }
  let result: R;
  try {
    result = await mutation.request();
  } catch (err) {
    if (previous !== undefined && mutation.optimistic) {
      const rollback = mutation.rollback ?? ((_current: T, before: T) => before);
      updateResource<T>(key, (current) => rollback(current as T, previous), client);
    }
    throw err;
  }
  if (client.getQueryData<T>(key) !== undefined) updateResource<T>(key, (current) => mutation.commit(current as T, result), client);
  return result;
}
