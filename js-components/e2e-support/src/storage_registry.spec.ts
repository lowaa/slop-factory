import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { expect, test, type Page } from "@playwright/test";

import { firstExistingPath } from "./first_existing_path";
import {
  assertStorageDeclared,
  auditStorage,
  grantedPurposes,
  loadStorageRegistry,
  observeStorage,
  storageProblems,
  type StorageEntry,
} from "./storage_registry";

function inventory(yaml: string): string {
  const path = join(mkdtempSync(join(tmpdir(), "inventory-")), "inventory.yaml");
  writeFileSync(path, yaml);
  return path;
}

const REGISTRY: StorageEntry[] = [
  { key: "session", type: "cookie", purpose: "necessary" },
  { key: "sid", type: "session_storage", purpose: "analytics" },
];

test.describe("loadStorageRegistry", () => {
  test("reads the storage section of the inventory", () => {
    const path = inventory("storage:\n  - {key: session, type: cookie, purpose: necessary}\nother: 1\n");
    expect(loadStorageRegistry(path)).toEqual([{ key: "session", type: "cookie", purpose: "necessary" }]);
  });

  test("refuses an inventory that declares no storage, which would let every audit pass", () => {
    expect(() => loadStorageRegistry(inventory("other: 1\n"))).toThrow(/declares no storage/);
    expect(() => loadStorageRegistry(inventory("storage: []\n"))).toThrow(/declares no storage/);
  });
});

test.describe("storageProblems", () => {
  test("names an undeclared key of any kind", () => {
    expect(
      storageProblems(
        [
          { key: "tracker", type: "cookie" },
          { key: "session", type: "local_storage" },
        ],
        REGISTRY,
        new Set(),
      ),
    ).toEqual(['undeclared cookie "tracker"', 'undeclared local_storage "session"']);
  });

  test("a key of a purpose that needs consent is a problem until it is granted; necessary never is", () => {
    const observed = [
      { key: "session", type: "cookie" as const },
      { key: "sid", type: "session_storage" as const },
    ];
    expect(storageProblems(observed, REGISTRY, new Set())).toEqual([
      'session_storage "sid" is set without consent for analytics',
    ]);
    expect(storageProblems(observed, REGISTRY, new Set(["analytics"]))).toEqual([]);
  });
});

test.describe("firstExistingPath", () => {
  test("takes the first candidate that exists, skipping unset ones", () => {
    const real = inventory("x: 1\n");
    expect(firstExistingPath("thing", [undefined, "/nonexistent", real, "/also/nonexistent"])).toBe(real);
  });

  test("lists what it tried when none exists", () => {
    expect(() => firstExistingPath("the inventory", [undefined, "/nonexistent"])).toThrow(
      "the inventory not found (tried /nonexistent)",
    );
  });
});

// Stand-ins that return what the test gives them: the browser's answers are
// the test's, the wiring from them to the problems is what is under test.
function fakePage(browser: { cookies: string[]; local: string[]; session: string[]; indexeddb: string[]; granted: string[] }) {
  return {
    context: () => ({
      cookies: async () => browser.cookies.map((name) => ({ name })),
      request: {
        get: async (url: string) => {
          expect(url).toBe("/api/privacy/consent");
          return { json: async () => ({ purposes: [{ id: "necessary", granted: true }, ...browser.granted.map((id) => ({ id, granted: true })), { id: "refused", granted: false }] }) };
        },
      },
    }),
    evaluate: async () => ({ local: browser.local, session: browser.session, indexeddb: browser.indexeddb }),
  } as unknown as Page;
}

test.describe("auditing a browser", () => {
  const registry: StorageEntry[] = [
    ...REGISTRY,
    { key: "pref", type: "local_storage", purpose: "preferences" },
    { key: "refused_thing", type: "indexeddb", purpose: "refused" },
  ];
  const empty = { cookies: [], local: [], session: [], indexeddb: [], granted: [] };

  test("observeStorage reports all four kinds, each with its own type", async () => {
    expect(
      await observeStorage(fakePage({ ...empty, cookies: ["c"], local: ["l"], session: ["s"], indexeddb: ["i"] })),
    ).toEqual([
      { key: "c", type: "cookie" },
      { key: "l", type: "local_storage" },
      { key: "s", type: "session_storage" },
      { key: "i", type: "indexeddb" },
    ]);
  });

  test("grantedPurposes is the purposes the server says are granted", async () => {
    const page = fakePage({ ...empty, granted: ["analytics"] });
    expect(await grantedPurposes(page.context())).toEqual(new Set(["necessary", "analytics"]));
  });

  test("auditStorage joins what is held, what is granted and the registry", async () => {
    const held = { ...empty, cookies: ["session", "stray"], local: ["pref"], indexeddb: ["refused_thing"] };
    expect(await auditStorage(fakePage(held), registry)).toEqual([
      'undeclared cookie "stray"',
      'local_storage "pref" is set without consent for preferences',
      'indexeddb "refused_thing" is set without consent for refused',
    ]);
    expect(await auditStorage(fakePage({ ...held, granted: ["preferences", "refused"] }), registry)).toEqual([
      'undeclared cookie "stray"',
    ]);
  });

  test("assertStorageDeclared passes when clean and names the step and every problem when not", async () => {
    await assertStorageDeclared(fakePage({ ...empty, cookies: ["session"] }), registry, "a clean step");
    await expect(
      assertStorageDeclared(fakePage({ ...empty, cookies: ["a", "b"] }), registry, "signing in"),
    ).rejects.toThrow('after signing in:\n  undeclared cookie "a"\n  undeclared cookie "b"');
  });
});
