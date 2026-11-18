import { describe, expect, it } from "vitest";
import { forgetPersonalState } from "@/lib/personal_state";
import { Drafts, MAX_DRAFTS, MAX_DRAFT_LENGTH, STORAGE_KEY, drafts } from "./drafts";

function memoryStorage(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    key: (i: number) => [...data.keys()][i] ?? null,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, String(v)),
    removeItem: (k: string) => void data.delete(k),
    clear: () => data.clear(),
  };
}

function fresh(storage: Storage, user = "u-1"): Drafts {
  const d = new Drafts(() => storage);
  d.bindUser(user);
  return d;
}

describe("Drafts", () => {
  it("keeps a draft per key and forgets one set to empty", () => {
    const d = fresh(memoryStorage());
    d.set("a", "one");
    d.set("b", "two");
    expect([d.get("a"), d.get("b")]).toEqual(["one", "two"]);
    d.set("a", "");
    expect(d.get("a")).toBe("");
    expect(d.get("b")).toBe("two");
  });

  it("is read back by a fresh store from the same storage", () => {
    const storage = memoryStorage();
    fresh(storage).set("a", "kept over a reload");
    expect(fresh(storage).get("a")).toBe("kept over a reload");
  });

  it("starts empty from a corrupt or foreign value, without throwing", () => {
    for (const raw of ["{not json", "null", '"x"', '{"user":"u-1","drafts":"nope"}', '{"user":"u-1","drafts":[["a",5],[1,"b"],"c"]}']) {
      const storage = memoryStorage();
      storage.setItem(STORAGE_KEY, raw);
      expect(fresh(storage).get("a")).toBe("");
    }
  });

  it("does not hand one user's drafts to another", () => {
    const storage = memoryStorage();
    fresh(storage, "u-1").set("a", "secret");
    const other = fresh(storage, "u-2");
    expect(other.get("a")).toBe("");
    expect(fresh(storage, "u-1").get("a")).toBe("");
  });

  it("clear empties memory and the storage key", () => {
    const storage = memoryStorage();
    const d = fresh(storage);
    d.set("a", "x");
    d.clear();
    expect(d.get("a")).toBe("");
    expect(storage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("drops the oldest drafts past the cap and truncates a long one", () => {
    const storage = memoryStorage();
    const d = fresh(storage);
    for (let i = 0; i <= MAX_DRAFTS; i++) d.set(`k${i}`, "x");
    expect(d.get("k0")).toBe("");
    expect(d.get(`k${MAX_DRAFTS}`)).toBe("x");
    d.set("long", "y".repeat(MAX_DRAFT_LENGTH + 5));
    expect(d.get("long")).toHaveLength(MAX_DRAFT_LENGTH);
    expect(JSON.parse(storage.getItem(STORAGE_KEY) ?? "").drafts).toHaveLength(MAX_DRAFTS);
  });

  it("counts an update as recent, so the next-oldest goes first", () => {
    const d = fresh(memoryStorage());
    for (let i = 0; i < MAX_DRAFTS; i++) d.set(`k${i}`, "x");
    d.set("k0", "updated");
    d.set("extra", "x");
    expect(d.get("k0")).toBe("updated");
    expect(d.get("k1")).toBe("");
  });

  it("still works in memory when storage throws or is missing", () => {
    const broken = { getItem: () => { throw new Error("no"); }, setItem: () => { throw new Error("no"); }, removeItem: () => { throw new Error("no"); } } as unknown as Storage;
    for (const d of [new Drafts(() => broken), new Drafts(() => undefined)]) {
      d.bindUser("u");
      d.set("a", "x");
      expect(d.get("a")).toBe("x");
      d.clear();
    }
  });
});

// Unsent messages may hold pasted secrets, so the next person to sign in
// must not find them.
it("the shared store is forgotten when the person signs out", () => {
  drafts.bindUser("u-1");
  drafts.set("new/c-1", "a pasted secret");

  forgetPersonalState();

  expect(drafts.get("new/c-1")).toBe("");
});
