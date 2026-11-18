import { useState, type ReactNode } from "react";
import { useConsentActions, useConsentSnapshot } from "./consent_context";
import "./cookie_preferences_button.scss";

interface DoNotSellButtonProps {
  // The purposes flagged SaleOrShare (go-privacy README §7). Today that is
  // marketing alone; an app that adds a vendor using the data for its own
  // purposes lists that purpose here too.
  purposes?: readonly string[];
  children?: ReactNode;
}

type Outcome = "saved" | "nothing_to_turn_off" | "failed" | null;

const MESSAGES: Record<Exclude<Outcome, null>, string> = {
  saved: "Your choice was saved.",
  nothing_to_turn_off: "No sale or sharing purpose is active, so there is nothing to turn off.",
  failed: "Your choice could not be saved. Try again.",
};

// The CCPA "Do Not Sell or Share My Personal Information" link: denies every
// SaleOrShare purpose in one step, recorded like any other decision (source
// settings), so it works without Global Privacy Control and is the same
// choice GPC makes. A button rather than a link, since it acts rather than
// navigates. Always rendered: the entry has to be there whatever is offered.
export default function DoNotSellButton({
  purposes = ["marketing"],
  children = "Do Not Sell or Share My Personal Information",
}: DoNotSellButtonProps) {
  const { decide } = useConsentActions();
  const snapshot = useConsentSnapshot();
  const [outcome, setOutcome] = useState<Outcome>(null);

  async function onClick() {
    // What is offered isn't known until the server has answered: an empty
    // list now would wrongly say there is nothing to turn off.
    if (snapshot.status !== "ready") {
      setOutcome("failed");
      return;
    }
    const offered = purposes.filter((id) => snapshot.purposes.some((p) => p.id === id));
    if (offered.length === 0) {
      setOutcome("nothing_to_turn_off");
      return;
    }
    const saved = await decide(
      "settings",
      offered.map((purpose) => ({ purpose, granted: false })),
    );
    setOutcome(saved ? "saved" : "failed");
  }

  return (
    <>
      <button type="button" className="cookie-preferences-button" disabled={snapshot.saving || snapshot.status === "loading"} onClick={() => void onClick()}>
        {children}
      </button>
      <span aria-live="polite" className="do-not-sell-outcome">
        {outcome ? MESSAGES[outcome] : ""}
      </span>
    </>
  );
}
