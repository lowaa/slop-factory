import { usePersisted } from "@slop-factory/storage/react";
import { browserStorage, type BrowserStorage } from "@/lib/browser_storage";
import { useCoarsePointer } from "@/lib/layout";

// useEnterSends is whether a plain Enter sends the message, per browser. Until
// the person chooses it is off where the primary input is touch (an on-screen
// keyboard has no Shift+Enter, so a newline could not be typed) and on
// elsewhere.
export function useEnterSends(storage: BrowserStorage = browserStorage()): [boolean, (next: boolean) => void] {
  const touch = useCoarsePointer();
  const { store, value } = storage.enterSends(!touch);
  return usePersisted(store, value);
}
