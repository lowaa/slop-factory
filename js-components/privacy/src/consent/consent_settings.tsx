import { Button } from "@slop-factory/ui";
import { useConsentActions, useConsentSnapshot, usePurposeCopy } from "./consent_context";
import { isCookieCategory } from "./consent_store";
import { PREFERENCES_TRIGGER_ATTRIBUTE } from "./focus";
import GpcNotice from "./gpc_notice";
import "./consent_settings.scss";

// The settings page's entry point: what is on and off now, and a button to
// change it.
export default function ConsentSettings() {
  const snapshot = useConsentSnapshot();
  const { openPreferences } = useConsentActions();
  const copyOf = usePurposeCopy();
  const purposes = snapshot.purposes.filter((p) => isCookieCategory(p) && !p.necessary);
  return (
    <section className="consent-settings" aria-labelledby="consent-settings-title">
      <h2 id="consent-settings-title" className="consent-settings__title">
        Cookie preferences
      </h2>
      <GpcNotice />
      <ul className="consent-settings__list">
        {purposes.map((p) => (
          <li key={p.id}>
            {copyOf(p).name}: <strong>{p.granted ? "On" : "Off"}</strong>
          </li>
        ))}
      </ul>
      <Button
        aria-haspopup="dialog"
        {...{ [PREFERENCES_TRIGGER_ATTRIBUTE]: "" }}
        onClick={() => openPreferences("settings")}
      >
        Change cookie preferences
      </Button>
    </section>
  );
}
