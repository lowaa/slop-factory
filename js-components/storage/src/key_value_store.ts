// KeyValueStore is small, string-valued persistence that never throws: the
// places browser storage lives refuse access (private windows, blocked site
// data, quota), and callers should not each have to guard against that.
export interface KeyValueStore {
  get(key: string): string | null;
  // set is false when the value was not kept beyond this visit: storage is
  // blocked or full.
  set(key: string, value: string): boolean;
  remove(key: string): void;
  // subscribe is told when the key's value changes, here or in another tab.
  subscribe(key: string, onChange: () => void): () => void;
}

// Listeners is the subscriber bookkeeping both stores share.
export class Listeners {
  private readonly byKey = new Map<string, Set<() => void>>();

  add(key: string, onChange: () => void): () => void {
    // A fresh wrapper per call, so the same callback subscribed twice is two
    // subscriptions, and each unsubscribe removes only its own.
    const entry = () => onChange();
    let set = this.byKey.get(key);
    if (!set) {
      set = new Set();
      this.byKey.set(key, set);
    }
    set.add(entry);
    return () => {
      set.delete(entry);
      if (set.size === 0 && this.byKey.get(key) === set) this.byKey.delete(key);
    };
  }

  notify(key: string): void {
    // Copied: a listener may unsubscribe while being notified.
    for (const entry of [...(this.byKey.get(key) ?? [])]) entry();
  }

  notifyAll(): void {
    for (const key of [...this.byKey.keys()]) this.notify(key);
  }

  get size(): number {
    return this.byKey.size;
  }
}
