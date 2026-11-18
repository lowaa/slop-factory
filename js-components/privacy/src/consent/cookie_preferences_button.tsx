import type { ReactNode } from "react";
import { useConsentActions } from "./consent_context";
import { PREFERENCES_TRIGGER_ATTRIBUTE } from "./focus";
import "./cookie_preferences_button.scss";

interface CookiePreferencesButtonProps {
  children?: ReactNode;
}

// The "Cookie preferences" entry in the footer (LegalFooterLinks' children
// slot takes it): always there, so a choice can be changed as easily as it
// was made. A button, not a link: it opens a dialog rather than going
// anywhere.
export default function CookiePreferencesButton({ children = "Cookie preferences" }: CookiePreferencesButtonProps) {
  const { openPreferences } = useConsentActions();
  return (
    <button
      type="button"
      className="cookie-preferences-button"
      aria-haspopup="dialog"
      {...{ [PREFERENCES_TRIGGER_ATTRIBUTE]: "" }}
      onClick={() => openPreferences("settings")}
    >
      {children}
    </button>
  );
}
