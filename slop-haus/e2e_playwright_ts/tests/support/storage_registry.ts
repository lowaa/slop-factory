import { firstExistingPath } from "@e2e-support/first_existing_path";
import {
  assertStorageDeclared as assertDeclared,
  auditStorage as audit,
  loadStorageRegistry,
  type StorageEntry,
} from "@e2e-support/storage_registry";
import type { Page } from "@playwright/test";

// The inventory is baked into the e2e image beside the tests (see its
// Dockerfile); a run from the checkout reads it where it lives.
function inventoryPath(): string {
  return firstExistingPath("slop-haus's data inventory", [
    process.env.INVENTORY_PATH,
    "/srv/inventory.yaml",
    new URL("../../../server/privacy/inventory.yaml", import.meta.url).pathname,
  ]);
}

// slop-haus's declared browser storage: what its inventory says the app may set,
// and under which purpose.
export function loadRegistry(): StorageEntry[] {
  return loadStorageRegistry(inventoryPath());
}

export const auditStorage = (page: Page) => audit(page, loadRegistry());
export const assertStorageDeclared = (page: Page, step: string) => assertDeclared(page, loadRegistry(), step);
