import type { Codec } from "./codecs.ts";
import type { KeyValueStore } from "./key_value_store.ts";
import type { StoredValue } from "./stored_value.ts";

export type StorageType = "local_storage" | "session_storage";

// The purposes in an app's privacy inventory (server/privacy/inventory.yaml)
// a key can be recorded under.
export type StoragePurpose = "necessary" | "analytics" | "marketing";

// StorageKey is one entry of an app's browser-storage inventory, in the
// shape the app's inventory test compares against inventory.yaml.
export interface StorageKey {
  key: string;
  type: StorageType;
  purpose: StoragePurpose;
}

export type BrowserStores = Record<StorageType, KeyValueStore>;

// BoundValue is a typed value with the store its key lives in, which is what
// usePersisted takes.
export interface BoundValue<T> {
  store: KeyValueStore;
  value: StoredValue<T>;
}

// bindKey pairs an inventory entry with the store its type names, so an
// app's accessors cannot read a key from the wrong one.
export function bindKey<T>(stores: BrowserStores, entry: StorageKey, codec: Codec<T>, fallback: T): BoundValue<T> {
  return { store: stores[entry.type], value: { key: entry.key, codec, fallback } };
}
