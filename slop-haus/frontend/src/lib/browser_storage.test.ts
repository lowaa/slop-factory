import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import { MemoryStore, readValue } from "@slop-factory/storage";
import { BrowserStorage } from "./browser_storage";

const INVENTORY = resolve(__dirname, "../../../server/privacy/inventory.yaml");

interface InventoryEntry {
  key: string;
  type: string;
  purpose: string;
}

function inventoryStorage(): InventoryEntry[] {
  const inventory = parse(readFileSync(INVENTORY, "utf8")) as { storage: InventoryEntry[] };
  return inventory.storage;
}

describe("BrowserStorage keys against the privacy inventory", () => {
  it("lists every key in server/privacy/inventory.yaml with the same type and purpose", () => {
    const listed = new Map(inventoryStorage().map((entry) => [entry.key, entry]));
    for (const { key, type, purpose } of BrowserStorage.keys) {
      const entry = listed.get(key);
      expect(entry, `${key} is missing from inventory.yaml's storage list`).toBeDefined();
      expect(entry?.type, `${key}'s storage type`).toBe(type);
      expect(entry?.purpose, `${key}'s purpose`).toBe(purpose);
    }
  });

  it("has no key twice", () => {
    const keys = BrowserStorage.keys.map((entry) => entry.key);
    expect(new Set(keys).size).toBe(keys.length);
  });
});

describe("BrowserStorage", () => {
  function setup() {
    const local = new MemoryStore();
    const session = new MemoryStore();
    return { local, session, storage: new BrowserStorage({ local_storage: local, session_storage: session }) };
  }

  it("reads the panel texts stored before the package existed", () => {
    const { local, storage } = setup();
    const { store, value } = storage.sessionsPanelOpen(true);
    expect(readValue(store, value)).toBe(true);
    local.set("remote_control.sessions_panel", "closed");
    expect(readValue(store, value)).toBe(false);
    local.set("remote_control.sessions_panel", "open");
    expect(readValue(store, value)).toBe(true);
  });

  it("keeps the panel in the store of its declared type", () => {
    const { local, session, storage } = setup();
    storage.sessionsPanelOpen(true).store.set("remote_control.sessions_panel", "closed");
    expect(local.get("remote_control.sessions_panel")).toBe("closed");
    expect(session.get("remote_control.sessions_panel")).toBeNull();
  });
});
