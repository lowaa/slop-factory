import { Button } from "@slop-factory/ui";
import { purposesNeedingBanner } from "./consent_store";
import { useConsentActions, useConsentSnapshot, useDocumentHref } from "./consent_context";
import { EQUAL_ACTION_PROPS } from "./equal_actions";
import GpcNotice from "./gpc_notice";
import { focusPreferencesTrigger } from "./focus";
import "./consent_banner.scss";

// Shown while any non-necessary cookie purpose needs a decision. It sits at
// the edge of the page instead of over it: no backdrop, no focus trap, so
// the app stays usable without a choice (no cookie wall).
export default function ConsentBanner() {
  const snapshot = useConsentSnapshot();
  const actions = useConsentActions();
  const href = useDocumentHref();
  if (snapshot.status !== "ready" || purposesNeedingBanner(snapshot.purposes).length === 0) return null;

  const choose = async (grant: boolean) => {
    const ok = await (grant ? actions.acceptAll("banner") : actions.rejectAll("banner"));
    if (ok) focusPreferencesTrigger();
  };

  return (
    <section className="consent-banner" aria-labelledby="consent-banner-title" aria-describedby="consent-banner-text">
      <h2 id="consent-banner-title" className="consent-banner__title">
        Your privacy choices
      </h2>
      <p id="consent-banner-text" className="consent-banner__text">
        We only use cookies that are strictly necessary unless you say otherwise. You can accept or reject the optional
        ones, or choose which. You can change your mind at any time.{" "}
        {href && (
          <a href={href("cookie_policy")} className="consent-banner__link">
            Read the cookie policy
          </a>
        )}
      </p>
      <GpcNotice />
      {snapshot.error && (
        <p role="alert" className="consent-banner__error">
          Your choice couldn't be saved. Please try again.
        </p>
      )}
      <div className="consent-banner__actions">
        <Button {...EQUAL_ACTION_PROPS} disabled={snapshot.saving} onClick={() => void choose(false)}>
          Reject all
        </Button>
        <Button {...EQUAL_ACTION_PROPS} disabled={snapshot.saving} onClick={() => void choose(true)}>
          Accept all
        </Button>
        <Button
          variant="ghost"
          disabled={snapshot.saving}
          aria-haspopup="dialog"
          onClick={() => actions.openPreferences("banner")}
        >
          Manage preferences
        </Button>
      </div>
    </section>
  );
}
