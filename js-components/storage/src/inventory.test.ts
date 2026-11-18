import { describe, expect, it } from "vitest";
import { boolCodec } from "./codecs.ts";
import { bindKey, type StorageKey } from "./inventory.ts";
import { MemoryStore } from "./memory_store.ts";

const LOCAL: StorageKey = { key: "a", type: "local_storage", purpose: "necessary" };
const SESSION: StorageKey = { key: "b", type: "session_storage", purpose: "necessary" };

describe("bindKey", () => {
  const stores = { local_storage: new MemoryStore(), session_storage: new MemoryStore() };

  it("picks the store the entry's type names", () => {
    expect(bindKey(stores, LOCAL, boolCodec("1", "0"), false).store).toBe(stores.local_storage);
    expect(bindKey(stores, SESSION, boolCodec("1", "0"), false).store).toBe(stores.session_storage);
  });

  it("carries the key, codec and fallback", () => {
    const codec = boolCodec("1", "0");
    expect(bindKey(stores, LOCAL, codec, true).value).toEqual({ key: "a", codec, fallback: true });
  });
});
