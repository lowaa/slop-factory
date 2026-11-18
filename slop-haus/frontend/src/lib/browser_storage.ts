import {
  bindKey,
  boolCodec,
  browserLocalStore,
  browserSessionStore,
  type BoundValue,
  type BrowserStores,
  type Codec,
  type StorageKey,
} from "@slop-factory/storage";

export type { BoundValue, BrowserStores, StorageKey, StoragePurpose, StorageType } from "@slop-factory/storage";

const SESSIONS_PANEL: StorageKey = {
  key: "remote_control.sessions_panel",
  type: "local_storage",
  purpose: "necessary",
};

const ENTER_SENDS: StorageKey = {
  key: "remote_control.enter_sends",
  type: "local_storage",
  purpose: "necessary",
};

// One codec instance, so the value a hook gets stays referentially stable
// and its setter is not remade on every render.
const SESSIONS_PANEL_CODEC = boolCodec("open", "closed");

const ENTER_SENDS_CODEC = boolCodec("on", "off");

// BrowserStorage is every key slop-haus keeps in the browser, with typed
// accessors so code never handles a raw key. A test checks `keys` against
// server/privacy/inventory.yaml, which must list each of them.
//
// The store is passed in -- BrowserStore in the app, MemoryStore in tests and
// stories. Consent is not checked here: every key is a necessary preference
// the person sets themselves. A key that needs consent takes a check from
// @slop-factory/privacy when the first one appears.
export class BrowserStorage {
  static readonly keys: readonly StorageKey[] = [SESSIONS_PANEL, ENTER_SENDS];

  private readonly stores: BrowserStores;

  constructor(stores: BrowserStores) {
    this.stores = stores;
  }

  // sessionsPanelOpen keeps the "open"/"closed" texts the panel stored
  // before this existed, so a choice already made still reads.
  sessionsPanelOpen(fallback: boolean): BoundValue<boolean> {
    return this.bind(SESSIONS_PANEL, SESSIONS_PANEL_CODEC, fallback);
  }

  enterSends(fallback: boolean): BoundValue<boolean> {
    return this.bind(ENTER_SENDS, ENTER_SENDS_CODEC, fallback);
  }

  private bind<T>(entry: StorageKey, codec: Codec<T>, fallback: T): BoundValue<T> {
    return bindKey(this.stores, entry, codec, fallback);
  }
}

let appStorage: BrowserStorage | null = null;

// browserStorage is the app's one instance, so every component on a key
// shares the same store. Made on first use: reading `window` at import
// would fail wherever there is none.
export function browserStorage(): BrowserStorage {
  appStorage ??= new BrowserStorage({
    local_storage: browserLocalStore(),
    session_storage: browserSessionStore(),
  });
  return appStorage;
}
