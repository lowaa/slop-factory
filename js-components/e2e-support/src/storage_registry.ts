import { readFileSync } from "node:fs";

import type { BrowserContext, Page } from "@playwright/test";
import { parse } from "yaml";

export type StorageType = "cookie" | "local_storage" | "session_storage" | "indexeddb";

export interface StorageEntry {
  key: string;
  type: StorageType;
  purpose: string;
}

export interface StoredKey {
  key: string;
  type: StorageType;
}

// The inventory's declared browser storage: what the app may set, and under
// which purpose. Read from the file the server's CI checks and the legal
// documents are generated from, so a key can't be declared for one and not
// the other.
export function loadStorageRegistry(inventoryPath: string): StorageEntry[] {
  const doc = parse(readFileSync(inventoryPath, "utf8")) as { storage?: StorageEntry[] };
  if (!doc.storage?.length) {
    throw new Error(`${inventoryPath} declares no storage: refusing to audit against an empty registry`);
  }
  return doc.storage;
}

// What the browser holds for the page's origin, of all four kinds. A cookie
// is listed by name whatever its path, so one scoped to a sub-path is seen
// too.
export async function observeStorage(page: Page): Promise<StoredKey[]> {
  const cookies = await page.context().cookies();
  const inPage = await page.evaluate(async () => {
    const keysOf = (s: Storage) => Array.from({ length: s.length }, (_, i) => s.key(i) as string);
    const databases = typeof indexedDB.databases === "function" ? await indexedDB.databases() : [];
    return {
      local: keysOf(localStorage),
      session: keysOf(sessionStorage),
      indexeddb: databases.map((d) => d.name ?? ""),
    };
  });
  return [
    ...cookies.map((c): StoredKey => ({ key: c.name, type: "cookie" })),
    ...inPage.local.map((key): StoredKey => ({ key, type: "local_storage" })),
    ...inPage.session.map((key): StoredKey => ({ key, type: "session_storage" })),
    ...inPage.indexeddb.map((key): StoredKey => ({ key, type: "indexeddb" })),
  ];
}

// The purposes the visitor has consented to, as the server reports them.
export async function grantedPurposes(context: BrowserContext): Promise<Set<string>> {
  const res = await context.request.get("/api/privacy/consent");
  const body = (await res.json()) as { purposes: { id: string; granted: boolean }[] };
  return new Set(body.purposes.filter((p) => p.granted).map((p) => p.id));
}

// What `observed` holds that the registry does not declare, and what is held
// without the consent its purpose needs. Necessary storage is disclosed, not
// consented to, so it needs no grant.
export function storageProblems(
  observed: readonly StoredKey[],
  registry: readonly StorageEntry[],
  granted: ReadonlySet<string>,
): string[] {
  const problems: string[] = [];
  for (const { type, key } of observed) {
    const entry = registry.find((e) => e.type === type && e.key === key);
    if (!entry) {
      problems.push(`undeclared ${type} ${JSON.stringify(key)}`);
    } else if (entry.purpose !== "necessary" && !granted.has(entry.purpose)) {
      problems.push(`${type} ${JSON.stringify(key)} is set without consent for ${entry.purpose}`);
    }
  }
  return problems;
}

// auditStorage lists the problems with what the browser holds now. Empty
// means clean. Run it on a page of the app, after the flow under test.
export async function auditStorage(page: Page, registry: readonly StorageEntry[]): Promise<string[]> {
  const [observed, granted] = await Promise.all([observeStorage(page), grantedPurposes(page.context())]);
  return storageProblems(observed, registry, granted);
}

// assertStorageDeclared fails, naming each key, if auditStorage finds any
// problem. `step` says where in a flow it was checked.
export async function assertStorageDeclared(page: Page, registry: readonly StorageEntry[], step: string): Promise<void> {
  const problems = await auditStorage(page, registry);
  if (problems.length > 0) throw new Error(`after ${step}:\n  ${problems.join("\n  ")}`);
}
