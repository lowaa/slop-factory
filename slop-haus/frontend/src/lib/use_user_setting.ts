import { createContext, useCallback, useContext, useRef, useState } from "react";
import { apiClient, type UserSetting } from "@/api";

// null until the read lands, and after one that failed: a slow or failed read
// leaves each setting at its default rather than blocking what it controls.
export const SettingsContext = createContext<{ provided: boolean; saved: UserSetting[] | null }>({ provided: false, saved: null });

export interface BoolUserSetting<T> {
  value: T;
  // Why the last change did not save; value keeps showing what was saved.
  error: string | null;
  change: (next: T) => void;
}

// useBoolUserSetting is one of the person's own on/off preferences. It shows
// fallback at once and the saved value when it arrives, so a slow or failed
// read never blocks what the preference controls; a change made before the
// read lands is not overwritten by it. A change that fails to save is rolled
// back, unless a later one has been made since.
export function useBoolUserSetting(name: string, fallback: boolean): BoolUserSetting<boolean> {
  const ctx = useContext(SettingsContext);
  if (!ctx.provided) throw new Error("useBoolUserSetting needs a UserSettingsProvider above it");

  // Set by the person's own change; wins over what was read, however late the
  // read lands.
  const [chosen, setChosen] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Which save is the latest, so an earlier one that fails late cannot roll
  // back a choice made after it.
  const latestSave = useRef(0);

  const saved = ctx.saved?.find((s) => s.name === name)?.bool_val;
  const value = chosen ?? (typeof saved === "boolean" ? saved : fallback);

  const change = useCallback(
    (next: boolean) => {
      // Back to what the person had chosen before, or to none, which shows
      // whatever the read found rather than a fallback it has since replaced.
      const previous = chosen;
      setChosen(next);
      setError(null);
      const save = ++latestSave.current;
      apiClient.setMyBoolSetting(name, next).catch(() => {
        if (save !== latestSave.current) return;
        setChosen(previous);
        setError("Could not save this preference.");
      });
    },
    [name, chosen],
  );

  return { value, error, change };
}
