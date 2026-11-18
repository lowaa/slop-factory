import { useConsentSnapshot } from "./consent_context";

// Says the signal was honoured, so a visitor who set it isn't left wondering
// why a purpose won't switch on.
export default function GpcNotice() {
  const { gpc } = useConsentSnapshot();
  if (!gpc.honoured) return null;
  return (
    <p className="consent-gpc-notice" data-testid="gpc-notice">
      Your browser's Global Privacy Control signal is honoured: purposes that sell or share your data stay off.
    </p>
  );
}
