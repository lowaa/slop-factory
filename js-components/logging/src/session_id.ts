import { BrowserStore, type KeyValueStore } from "@slop-factory/storage";

const STORAGE_KEY = "slop-factory-logging:session-id";

// Not browserSessionStore(): that reads `window` when built, and a logger can
// be constructed where there is none (SSR, tests, a worker). Here the global
// is only looked up on use, where BrowserStore treats its absence like
// blocked storage.
function defaultStore(): KeyValueStore {
  return new BrowserStore({ storage: () => sessionStorage, events: globalThis });
}

// crypto.randomUUID only exists in secure contexts (https, localhost), and an
// app can be served over plain http on a private host; getRandomValues is
// available everywhere.
export function randomId(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// A random id for one browser session, so records from the same session can
// be grouped without identifying a person. It is made fresh per session and
// is never derived from a user.
//
// Persisting it is storage that needs analytics consent (go-privacy/README.md
// §8), so it is written to the session store only while the consent check
// passes; otherwise it lives in memory only.
export class SessionId {
  private memo: string | undefined;
  private persisted = false;
  private readonly mayPersist: () => boolean;
  private readonly store: KeyValueStore;

  constructor(mayPersist: () => boolean, store: KeyValueStore = defaultStore()) {
    this.mayPersist = mayPersist;
    this.store = store;
  }

  get(): string {
    if (this.mayPersist()) {
      const stored = this.readStored();
      if (stored) {
        // Adopted from storage (another logger's, or an earlier page's): it is
        // the stored id now, and goes with it when consent is withdrawn.
        this.persisted = true;
        return (this.memo = stored);
      }
      this.memo ??= randomId();
      this.writeStored(this.memo);
      this.persisted = true;
      return this.memo;
    }
    this.sync();
    return (this.memo ??= randomId());
  }

  // Withdrawing consent removes what was stored and forgets the id, so an id
  // that was consented to is not carried into whatever is logged next, and a
  // later opt-in gets a new one. An id that was never stored (made before
  // consent) is kept: nothing about it needed consent.
  sync(): void {
    if (this.mayPersist() || (!this.persisted && this.readStored() === null)) return;
    this.removeStored();
    this.memo = undefined;
    this.persisted = false;
  }

  private readStored(): string | null {
    return this.store.get(STORAGE_KEY);
  }

  private removeStored(): void {
    this.store.remove(STORAGE_KEY);
  }

  // A refused write leaves the id in memory only, which still works.
  private writeStored(id: string): void {
    this.store.set(STORAGE_KEY, id);
  }
}
