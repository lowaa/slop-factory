import { browserSessionStore, type KeyValueStore } from "@slop-factory/storage";

import type { MethodAction } from "./types";

// A step-up through a provider leaves the page, so the change it was for
// has to outlive the page to be offered again on the way back. Kept in
// sessionStorage -- this tab only, gone when it closes -- and never with a
// password in it: a password change is re-typed after a step-up, not
// replayed from storage.
const KEY = "slop-factory-auth:pending-step-up";

// The step-up window both servers allow is ten minutes; an entry older
// than that could only be a leftover from an abandoned attempt.
const MAX_AGE_MS = 10 * 60 * 1000;

export type StoredAction =
  | { kind: "add-password" }
  | { kind: "change-password" }
  | { kind: "remove-password" }
  | { kind: "remove-identity"; provider: string; subject: string }
  | { kind: "link"; provider: string };

export function withoutSecrets(action: MethodAction): StoredAction {
  switch (action.kind) {
    case "add-password":
    case "change-password":
      return { kind: action.kind };
    default:
      return action;
  }
}

export function stashPendingStepUp(action: MethodAction, store: KeyValueStore = browserSessionStore()): void {
  // A refused write (a locked-down browser) is ignored: the step-up still
  // works, the person just starts the change again afterwards.
  store.set(KEY, JSON.stringify({ at: Date.now(), action: withoutSecrets(action) }));
}

// peekPendingStepUp returns the stashed change, if a fresh one is there, and
// leaves it in place -- unlike a take/consume, reading this is safe to do
// more than once. Split from clearing (below) because the one place this is
// read is a useState initializer, which React's StrictMode calls twice on
// mount to surface impure initializers: a combined read-and-clear there
// clears the entry on the first call and finds nothing on the second, so
// whichever result "wins" is a coin flip -- silently losing the resume half
// the time. Callers clear it themselves, once, from an effect instead.
export function peekPendingStepUp(store: KeyValueStore = browserSessionStore()): StoredAction | null {
  const raw = store.get(KEY);
  if (!raw) return null;
  try {
    const { at, action } = JSON.parse(raw) as { at: number; action: StoredAction };
    return typeof at === "number" && Date.now() - at < MAX_AGE_MS ? action : null;
  } catch {
    return null;
  }
}

// clearPendingStepUp removes the stashed change, whether or not it was
// (still) there to remove -- it is offered once.
export function clearPendingStepUp(store: KeyValueStore = browserSessionStore()): void {
  store.remove(KEY);
}
