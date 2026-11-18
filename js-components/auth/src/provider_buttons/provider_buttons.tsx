import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { LoginProvider } from "../config";
import "./provider_buttons.scss";

interface ProviderButtonsProps {
  providers: LoginProvider[];
  label: (displayName: string) => string;
  // What choosing one does: navigate to a sign-in URL, or start a round
  // trip that answers with where to send the browser. A rejection re-enables
  // the buttons; the caller shows the error.
  onChoose: (provider: LoginProvider) => void | Promise<void>;
  disabled?: boolean;
}

// One "Continue with <provider>" button per provider -- the sign-in page,
// an invite, and the step-up prompt all offer providers the same way.
// While one is being started every button is disabled, so a second click
// can't start a second round trip whose state cookie replaces the first.
export default function ProviderButtons({ providers, label, onChoose, disabled }: ProviderButtonsProps) {
  const [busy, setBusy] = useState(false);
  if (providers.length === 0) return null;

  async function choose(provider: LoginProvider) {
    setBusy(true);
    try {
      await onChoose(provider);
    } catch {
      setBusy(false);
    }
  }

  return (
    <div className="provider-buttons">
      {providers.map((p) => (
        <Button
          key={p.name}
          className="provider-buttons__button"
          disabled={disabled || busy}
          onClick={() => void choose(p)}
        >
          {label(p.displayName)}
        </Button>
      ))}
    </div>
  );
}

// OrDivider separates the provider buttons from a password form.
export function OrDivider({ label }: { label: string }) {
  return (
    <p className="provider-buttons__divider">
      <span>{label}</span>
    </p>
  );
}
