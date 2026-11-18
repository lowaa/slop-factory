import { useCallback, useMemo, useSyncExternalStore } from "react";
import type { KeyValueStore } from "./key_value_store.ts";
import { decodeStored, fallbackOf, writeValue, type StoredValue } from "./stored_value.ts";

// usePersisted is a stored value as React state. The fallback applies until
// the person chooses, and nothing is written before they do. Every component
// on the same store and key stays in step, across tabs too.
export function usePersisted<T>(
  store: KeyValueStore,
  value: StoredValue<T>,
): [T, (next: T) => void] {
  const { key, codec } = value;
  const subscribe = useCallback(
    (onChange: () => void) => store.subscribe(key, onChange),
    [store, key],
  );
  // The raw text is the snapshot: a decoded object would be new on every read
  // and re-render forever.
  const text = useSyncExternalStore(
    subscribe,
    () => store.get(key),
    // Nothing is stored where there is no browser: the fallback applies.
    () => null,
  );
  // value is often built inline, so its identity says nothing; what it
  // decodes from is the key, the codec and the fallback's result.
  const fallback = fallbackOf(value);
  const current = useMemo(
    () => decodeStored({ key, codec, fallback }, text),
    [key, codec, fallback, text],
  );
  const set = useCallback(
    (next: T) => void writeValue(store, { key, codec, fallback }, next),
    [store, key, codec, fallback],
  );
  return [current, set];
}
