import { useCallback, useState } from "react";
import { useBoolUserSetting } from "./use_user_setting";

// The server's name for the setting; on unless the person turned it off.
export const RENDER_MARKDOWN = "render_markdown";

export interface RenderMarkdown {
  // Whether to render markdown here: the page's override if one was made,
  // else the saved setting.
  value: boolean;
  // The saved setting, whatever this page shows.
  saved: boolean;
  // Flips this page only; nothing is saved, and a reload shows the saved value.
  toggleForPage: () => void;
  // Why the last save failed, from the setting itself.
  error: string | null;
  // Saves the setting, and drops this page's override so the person sees what
  // they just chose.
  change: (next: boolean) => void;
}

// useRenderMarkdown needs a UserSettingsProvider above it.
export function useRenderMarkdown(): RenderMarkdown {
  const setting = useBoolUserSetting(RENDER_MARKDOWN, true);
  const [override, setOverride] = useState<boolean | null>(null);

  const toggleForPage = useCallback(() => setOverride((o) => !(o ?? setting.value)), [setting.value]);
  const change = useCallback(
    (next: boolean) => {
      setOverride(null);
      setting.change(next);
    },
    [setting],
  );

  return { value: override ?? setting.value, saved: setting.value, toggleForPage, error: setting.error, change };
}
