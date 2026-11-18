import type { BuildTag } from "./build_tag";

// What each engine says when a lazily loaded chunk can't be fetched: Chrome
// and Safari-adjacent "Failed to fetch dynamically imported module", Safari's
// "Importing a module script failed", Firefox's "error loading dynamically
// imported module". Vite's preload helper adds "Unable to preload CSS".
const CHUNK_MESSAGE =
  /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS/i;

export function isChunkLoadError(err: unknown): boolean {
  return err instanceof Error && (err.name === "ChunkLoadError" || CHUNK_MESSAGE.test(err.message));
}

export type ReloadAttempt = "reloading" | "blocked";

export interface ChunkRecoveryOptions {
  buildTag: BuildTag;
  storage?: Pick<Storage, "getItem" | "setItem">;
  reload?: () => void;
  now?: () => number;
  path?: () => string;
  guardMs?: number;
}

const GUARD_PREFIX = "chunk-reload:";

// A failed chunk is almost always a deploy that replaced the build the tab
// runs, which a reload fixes. It is tried once per path and build tag within
// guardMs, so when the failure is real (the server is down) the page stops at
// a message instead of reloading forever.
export class ChunkRecovery {
  private readonly o: Required<ChunkRecoveryOptions>;
  private reloading = false;

  constructor(options: ChunkRecoveryOptions) {
    this.o = {
      storage: safeSessionStorage(),
      reload: () => window.location.reload(),
      now: Date.now,
      path: () => window.location.pathname,
      guardMs: 60_000,
      ...options,
    };
  }

  // Whether attempt() would reload, without doing it.
  canReload(): boolean {
    return this.reloading || !this.guarded();
  }

  attempt(): ReloadAttempt {
    if (this.reloading) return "reloading";
    if (this.guarded()) return "blocked";
    try {
      this.o.storage.setItem(this.keys()[0], String(this.o.now()));
    } catch {
      // No storage means no guard, so a reload could loop: don't.
      return "blocked";
    }
    this.reloading = true;
    this.o.reload();
    return "reloading";
  }

  // The guard is read under the tag and under "unknown": a failure before the
  // tag was read and one after it on the reloaded page are the same incident.
  private keys(): string[] {
    const path = this.o.path();
    return [...new Set([this.o.buildTag.peek() ?? "unknown", "unknown"])].map((tag) => `${GUARD_PREFIX}${path}|${tag}`);
  }

  private guarded(): boolean {
    try {
      return this.keys().some((key) => {
        const at = Number(this.o.storage.getItem(key));
        return Number.isFinite(at) && at > 0 && this.o.now() - at < this.o.guardMs;
      });
    } catch {
      return true;
    }
  }
}

function safeSessionStorage(): Pick<Storage, "getItem" | "setItem"> {
  try {
    return window.sessionStorage;
  } catch {
    // Blocked storage: every read is "guarded", so nothing reloads.
    return { getItem: () => String(Date.now()), setItem: () => {} };
  }
}
