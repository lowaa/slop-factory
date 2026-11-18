import { useEffect, useState } from "react";
import { apiClient, type FleetInstructions, type Setting } from "@/api";
import { EMPTY_WORD_LIST_WARNING, judgmentAboveHuman, pinnedWordListEmpty } from "./kind_copy";

// WordListWarning is advice only: nothing blocks a save. The steward settings
// are read here, not passed in, so the widget stays usable on its own; a
// failed read leaves the warning out.
export default function WordListWarning({ data }: { data: FleetInstructions }) {
  const [settings, setSettings] = useState<Setting[] | null>(null);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .listSettings()
      .then((s) => {
        if (!cancelled) setSettings(s);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  if (!settings || !pinnedWordListEmpty(data) || !judgmentAboveHuman(settings)) return null;
  return (
    <p role="alert" className="fleet-instructions__error">
      {EMPTY_WORD_LIST_WARNING}
    </p>
  );
}
