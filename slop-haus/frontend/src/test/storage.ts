import { onTestFinished, vi } from "vitest";

// stubLocalStorage gives this test its own localStorage that starts empty,
// whatever earlier tests in the file left in jsdom's. Undone when the test ends.
export function stubLocalStorage(): Storage {
  const data = new Map<string, string>();
  const storage = {
    get length() {
      return data.size;
    },
    key: (i: number) => [...data.keys()][i] ?? null,
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => void data.set(key, String(value)),
    removeItem: (key: string) => void data.delete(key),
    clear: () => data.clear(),
  } satisfies Storage;
  vi.stubGlobal("localStorage", storage);
  onTestFinished(() => {
    vi.unstubAllGlobals();
  });
  return storage;
}
