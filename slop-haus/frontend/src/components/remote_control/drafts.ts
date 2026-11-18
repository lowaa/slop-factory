import { registerPersonalState } from "@/lib/personal_state";

export const MAX_DRAFTS = 20;
export const MAX_DRAFT_LENGTH = 20_000;
export const STORAGE_KEY = "slop-haus:remote-control-drafts";

export const sessionDraftKey = (conductorId: string, sessionId: string) => `session/${conductorId}/${sessionId}`;
export const newSessionDraftKey = (conductorId: string) => `new/${conductorId}`;

interface Persisted {
  user: string;
  drafts: [string, string][];
}

// Drafts is the unsent text of the Remote Control message boxes, kept above
// the router so it outlives the page's unmounting when the person visits
// another tab. It is mirrored to sessionStorage, never localStorage: a draft
// can hold a pasted secret, so it must die with the browser tab (the same
// reasoning as pending_invite.ts). It is bound to the signed-in user so that
// a session that merely expires does not hand one person's drafts to the next.
export class Drafts {
  // Oldest first: setting a key moves it last, and the cap drops from the front.
  private readonly drafts = new Map<string, string>();
  private user = "";

  private readonly storage: () => Storage | undefined;

  constructor(storage: () => Storage | undefined) {
    this.storage = storage;
  }

  get(key: string): string {
    return this.drafts.get(key) ?? "";
  }

  // An empty text forgets the key.
  set(key: string, text: string): void {
    const next = text.slice(0, MAX_DRAFT_LENGTH);
    if (next === this.get(key)) return;
    this.drafts.delete(key);
    if (next !== "") {
      this.drafts.set(key, next);
      while (this.drafts.size > MAX_DRAFTS) this.drafts.delete(this.drafts.keys().next().value as string);
    }
    this.persist();
  }

  // Called once the person is known, and safe to repeat. Drafts stored for
  // anyone else are dropped.
  bindUser(user: string): void {
    if (user === this.user) return;
    this.user = user;
    this.drafts.clear();
    for (const [key, text] of this.read(user)) this.drafts.set(key, text);
    if (this.drafts.size === 0) this.persist();
  }

  clear(): void {
    this.drafts.clear();
    this.user = "";
    try {
      this.storage()?.removeItem(STORAGE_KEY);
    } catch {
      // Storage that refuses is a store that remembers nothing.
    }
  }

  // Tolerant of anything: a missing, corrupt or foreign value is an empty store.
  private read(user: string): [string, string][] {
    try {
      const raw = this.storage()?.getItem(STORAGE_KEY);
      if (!raw) return [];
      const parsed = JSON.parse(raw) as Partial<Persisted> | null;
      if (!parsed || parsed.user !== user || !Array.isArray(parsed.drafts)) return [];
      return parsed.drafts
        .filter((d): d is [string, string] => Array.isArray(d) && typeof d[0] === "string" && typeof d[1] === "string" && d[1] !== "")
        .map(([k, t]): [string, string] => [k, t.slice(0, MAX_DRAFT_LENGTH)])
        .slice(-MAX_DRAFTS);
    } catch {
      return [];
    }
  }

  private persist(): void {
    try {
      const storage = this.storage();
      if (!storage) return;
      if (this.drafts.size === 0) storage.removeItem(STORAGE_KEY);
      else storage.setItem(STORAGE_KEY, JSON.stringify({ user: this.user, drafts: [...this.drafts] } satisfies Persisted));
    } catch {
      // Quota or blocked storage: the in-memory copy still serves the page.
    }
  }
}

function sessionStorageOrUndefined(): Storage | undefined {
  try {
    return window.sessionStorage;
  } catch {
    return undefined;
  }
}

export const drafts = new Drafts(sessionStorageOrUndefined);

// Unsent messages, which may hold pasted secrets.
registerPersonalState("remote control drafts", { forget: () => drafts.clear(), bindUser: (userId) => drafts.bindUser(userId) });
