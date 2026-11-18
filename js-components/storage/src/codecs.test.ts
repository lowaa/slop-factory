import { describe, expect, it } from "vitest";
import { boolCodec, enumCodec, jsonCodec } from "./codecs.ts";
import { decodeStored, readValue, writeValue, type StoredValue } from "./stored_value.ts";
import { MemoryStore } from "./memory_store.ts";

describe("codecs", () => {
  it("boolCodec round-trips and reads the existing texts", () => {
    const codec = boolCodec("open", "closed");
    expect(codec.encode(true)).toBe("open");
    expect(codec.encode(false)).toBe("closed");
    expect(codec.decode("open")).toBe(true);
    expect(codec.decode("closed")).toBe(false);
    expect(codec.decode("1")).toBeUndefined();
  });

  it("enumCodec round-trips and rejects others", () => {
    const codec = enumCodec(["a", "b"] as const);
    expect(codec.decode(codec.encode("b"))).toBe("b");
    expect(codec.decode("c")).toBeUndefined();
  });

  it("jsonCodec round-trips and rejects bad JSON and wrong shapes", () => {
    const codec = jsonCodec((v): v is { n: number } => typeof v === "object" && v !== null && typeof (v as { n?: unknown }).n === "number");
    expect(codec.decode(codec.encode({ n: 3 }))).toEqual({ n: 3 });
    expect(codec.decode("{not json")).toBeUndefined();
    expect(codec.decode('{"n":"x"}')).toBeUndefined();
  });
});

describe("StoredValue", () => {
  const value: StoredValue<boolean> = {
    key: "k",
    codec: boolCodec("open", "closed"),
    fallback: true,
  };

  it("reads the fallback for nothing stored and for undecodable text", () => {
    const store = new MemoryStore();
    expect(readValue(store, value)).toBe(true);
    store.set("k", "garbage");
    expect(readValue(store, value)).toBe(true);
    expect(decodeStored(value, null)).toBe(true);
  });

  it("evaluates a function fallback on each read", () => {
    let wide = true;
    const device: StoredValue<boolean> = { ...value, fallback: () => wide };
    const store = new MemoryStore();
    expect(readValue(store, device)).toBe(true);
    wide = false;
    expect(readValue(store, device)).toBe(false);
  });

  it("writes through the codec", () => {
    const store = new MemoryStore();
    expect(writeValue(store, value, false)).toBe(true);
    expect(store.get("k")).toBe("closed");
    expect(readValue(store, value)).toBe(false);
  });
});
