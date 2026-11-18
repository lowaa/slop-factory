import { useEffect, useState, type ReactNode } from "react";
import { apiClient, type UserSetting } from "@/api";
import { SettingsContext } from "./use_user_setting";

// UserSettingsProvider reads the person's settings once for everything below
// it, so a page whose parts each show a preference fetches them all once, not
// once per part.
export function UserSettingsProvider({ children }: { children: ReactNode }) {
  const [saved, setSaved] = useState<UserSetting[] | null>(null);

  useEffect(() => {
    let live = true;
    apiClient
      .listMySettings()
      .then((settings) => {
        if (live) setSaved(settings);
      })
      // Quiet: settings fall back to their defaults.
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  return <SettingsContext.Provider value={{ provided: true, saved }}>{children}</SettingsContext.Provider>;
}
