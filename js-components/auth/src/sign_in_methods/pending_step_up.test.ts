import { afterEach, describe, expect, it } from "vitest";
import { BrowserStore, MemoryStore } from "@slop-factory/storage";
import { clearPendingStepUp, peekPendingStepUp, stashPendingStepUp } from "./pending_step_up";

afterEach(() => {
  sessionStorage.clear();
});

describe("pending_step_up", () => {
  it("returns the stashed change without removing it", () => {
    stashPendingStepUp({ kind: "link", provider: "github" });

    expect(peekPendingStepUp()).toEqual({ kind: "link", provider: "github" });
    // A second peek sees the same thing -- unlike a take, peeking is
    // repeatable, which is exactly what lets a useState initializer be
    // called twice (StrictMode) without losing the value.
    expect(peekPendingStepUp()).toEqual({ kind: "link", provider: "github" });
  });

  it("drops a password's own secret when stashing it, keeping only its kind", () => {
    stashPendingStepUp({ kind: "add-password", email: "ada@example.com", password: "never-stored" });

    expect(peekPendingStepUp()).toEqual({ kind: "add-password" });
  });

  it("clears the stash so a later peek finds nothing", () => {
    stashPendingStepUp({ kind: "remove-password" });

    clearPendingStepUp();

    expect(peekPendingStepUp()).toBeNull();
  });

  // The step-up window both servers allow is ten minutes; an entry older
  // than that could only be a leftover from an abandoned attempt, so it's
  // not offered back as if it were fresh.
  it("treats an entry older than the step-up window as gone", () => {
    const stale = { at: Date.now() - 11 * 60 * 1000, action: { kind: "remove-password" } };
    sessionStorage.setItem("slop-factory-auth:pending-step-up", JSON.stringify(stale));

    expect(peekPendingStepUp()).toBeNull();
  });

  it("still offers an entry just inside the step-up window", () => {
    const fresh = { at: Date.now() - 9 * 60 * 1000, action: { kind: "remove-password" } };
    sessionStorage.setItem("slop-factory-auth:pending-step-up", JSON.stringify(fresh));

    expect(peekPendingStepUp()).toEqual({ kind: "remove-password" });
  });

  it("keeps the stash in the store it is given, under the same key and format", () => {
    const store = new MemoryStore();
    stashPendingStepUp({ kind: "change-password", currentPassword: "never-stored", newPassword: "never-stored" } as never, store);

    const raw = JSON.parse(store.get("slop-factory-auth:pending-step-up")!);
    expect(raw.action).toEqual({ kind: "change-password" });
    expect(typeof raw.at).toBe("number");
    expect(peekPendingStepUp(store)).toEqual({ kind: "change-password" });
    expect(sessionStorage.length).toBe(0);
    clearPendingStepUp(store);
    expect(peekPendingStepUp(store)).toBeNull();
  });

  it("treats unreadable stored text as nothing", () => {
    const store = new MemoryStore();
    store.set("slop-factory-auth:pending-step-up", "not json");

    expect(peekPendingStepUp(store)).toBeNull();
  });

  it("holds the stash in the store's memory when the browser refuses storage", () => {
    const store = new BrowserStore({
      storage: () => {
        throw new DOMException("blocked", "SecurityError");
      },
      events: { addEventListener: () => {}, removeEventListener: () => {} },
    });

    stashPendingStepUp({ kind: "remove-password" }, store);
    expect(peekPendingStepUp(store)).toEqual({ kind: "remove-password" });
    clearPendingStepUp(store);
    expect(peekPendingStepUp(store)).toBeNull();
  });
});
