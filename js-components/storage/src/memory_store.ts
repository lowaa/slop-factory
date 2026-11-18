import { Listeners, type KeyValueStore } from "./key_value_store.ts";

// MemoryStore keeps values for the life of the object: for tests and
// stories, and as the shape of what BrowserStore falls back to.
export class MemoryStore implements KeyValueStore {
  private readonly data = new Map<string, string>();
  private readonly listeners = new Listeners();

  get(key: string): string | null {
    return this.data.get(key) ?? null;
  }

  set(key: string, value: string): boolean {
    this.data.set(key, value);
    this.listeners.notify(key);
    return true;
  }

  remove(key: string): void {
    if (this.data.delete(key)) this.listeners.notify(key);
  }

  subscribe(key: string, onChange: () => void): () => void {
    return this.listeners.add(key, onChange);
  }
}
