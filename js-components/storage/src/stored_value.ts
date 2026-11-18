import type { Codec } from "./codecs.ts";
import type { KeyValueStore } from "./key_value_store.ts";

// StoredValue names one typed value in a store. The fallback is what it
// reads as until a choice is stored; a function lets the default depend on
// the device.
export interface StoredValue<T> {
  key: string;
  codec: Codec<T>;
  fallback: T | (() => T);
}

export function fallbackOf<T>(value: StoredValue<T>): T {
  return typeof value.fallback === "function" ? (value.fallback as () => T)() : value.fallback;
}

// decodeStored is the value for raw stored text: the fallback when there is
// none or it will not decode.
export function decodeStored<T>(value: StoredValue<T>, text: string | null): T {
  const decoded = text === null ? undefined : value.codec.decode(text);
  return decoded === undefined ? fallbackOf(value) : decoded;
}

export function readValue<T>(store: KeyValueStore, value: StoredValue<T>): T {
  return decodeStored(value, store.get(value.key));
}

export function writeValue<T>(store: KeyValueStore, value: StoredValue<T>, next: T): boolean {
  return store.set(value.key, value.codec.encode(next));
}
