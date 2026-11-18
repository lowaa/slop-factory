import type { StorageEvents } from "./browser_store.ts";

// FakeStorage is a Storage that can be made to refuse, as a browser does
// with site data blocked or the quota used up.
export class FakeStorage implements Storage {
  readonly data = new Map<string, string>();
  failGet = false;
  failSet = false;
  failRemove = false;

  get length(): number {
    return this.data.size;
  }
  key(index: number): string | null {
    return [...this.data.keys()][index] ?? null;
  }
  getItem(key: string): string | null {
    if (this.failGet) throw new DOMException("denied", "SecurityError");
    return this.data.get(key) ?? null;
  }
  setItem(key: string, value: string): void {
    if (this.failSet) throw new DOMException("full", "QuotaExceededError");
    this.data.set(key, value);
  }
  removeItem(key: string): void {
    if (this.failRemove) throw new DOMException("denied", "SecurityError");
    this.data.delete(key);
  }
  clear(): void {
    this.data.clear();
  }
}

// FakeWindow is the event source of a tab, and can deliver another tab's
// storage events.
export class FakeWindow implements StorageEvents {
  private readonly target = new EventTarget();
  listenerCount = 0;

  addEventListener(_type: "storage", listener: (event: StorageEvent) => void): void {
    this.listenerCount++;
    this.target.addEventListener("storage", listener as EventListener);
  }
  removeEventListener(_type: "storage", listener: (event: StorageEvent) => void): void {
    this.listenerCount--;
    this.target.removeEventListener("storage", listener as EventListener);
  }
  storageEvent(key: string | null, area: Storage | null): void {
    this.target.dispatchEvent(Object.assign(new Event("storage"), { key, storageArea: area }));
  }
}
