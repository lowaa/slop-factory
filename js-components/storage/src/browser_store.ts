import { Listeners, type KeyValueStore } from "./key_value_store.ts";

// What BrowserStore needs of the window, so tests can inject their own.
export interface StorageEvents {
  addEventListener(type: "storage", listener: (event: StorageEvent) => void): void;
  removeEventListener(type: "storage", listener: (event: StorageEvent) => void): void;
}

export interface BrowserStoreOptions {
  // Looked up on every access, never cached: merely reading
  // `window.localStorage` throws a SecurityError when site data is blocked,
  // and access can be revoked after the page loads.
  storage: () => Storage;
  events: StorageEvents;
}

// BrowserStore is a KeyValueStore over localStorage or sessionStorage that
// never throws. A write storage refuses is kept in memory for the visit, so
// the choice still holds until the page closes.
export class BrowserStore implements KeyValueStore {
  // A key maps to the value to show instead of storage's; null means removed.
  private readonly overlay = new Map<string, string | null>();
  private readonly listeners = new Listeners();
  private listening = false;
  // The last storage seen working. An event can arrive while access is
  // revoked; dropping it would leave a value held only in memory hiding the
  // other tab's write once access returns.
  private lastArea: Storage | null = null;

  private readonly options: BrowserStoreOptions;

  constructor(options: BrowserStoreOptions) {
    this.options = options;
  }

  get(key: string): string | null {
    if (this.overlay.has(key)) return this.overlay.get(key) ?? null;
    try {
      return this.area().getItem(key);
    } catch {
      return null;
    }
  }

  set(key: string, value: string): boolean {
    let kept = true;
    try {
      this.area().setItem(key, value);
      this.overlay.delete(key);
    } catch {
      this.overlay.set(key, value);
      kept = false;
    }
    this.listeners.notify(key);
    return kept;
  }

  remove(key: string): void {
    try {
      this.area().removeItem(key);
      this.overlay.delete(key);
    } catch {
      // Hides whatever storage still holds for the rest of the visit.
      this.overlay.set(key, null);
    }
    this.listeners.notify(key);
  }

  subscribe(key: string, onChange: () => void): () => void {
    const unsubscribe = this.listeners.add(key, onChange);
    this.listen();
    return () => {
      unsubscribe();
      if (this.listeners.size === 0) this.stopListening();
    };
  }

  private readonly onStorageEvent = (event: StorageEvent): void => {
    if (!this.isOurs(event.storageArea)) return;
    // A null key is storage.clear().
    if (event.key === null) {
      this.overlay.clear();
      this.listeners.notifyAll();
      return;
    }
    // Another tab's write is newer than anything held only in this one.
    this.overlay.delete(event.key);
    this.listeners.notify(event.key);
  };

  // The event also fires for the other storage area of the same origin.
  private isOurs(area: Storage | null): boolean {
    if (area === null) return true;
    try {
      return area === this.area();
    } catch {
      return area === this.lastArea;
    }
  }

  private area(): Storage {
    this.lastArea = this.options.storage();
    return this.lastArea;
  }

  private listen(): void {
    if (this.listening) return;
    this.listening = true;
    this.options.events.addEventListener("storage", this.onStorageEvent);
  }

  private stopListening(): void {
    if (!this.listening) return;
    this.listening = false;
    this.options.events.removeEventListener("storage", this.onStorageEvent);
  }
}

// browserLocalStore and browserSessionStore are the window's own storages.
export function browserLocalStore(): BrowserStore {
  return new BrowserStore({ storage: () => window.localStorage, events: window });
}

export function browserSessionStore(): BrowserStore {
  return new BrowserStore({ storage: () => window.sessionStorage, events: window });
}
