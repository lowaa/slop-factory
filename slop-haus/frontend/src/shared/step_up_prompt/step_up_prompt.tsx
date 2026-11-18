import { useMemo, useState } from "react";
import {
  StepUpPrompt as SharedStepUpPrompt,
  defaultSignInMethodsCopy,
  redirectTo,
  stepUpMethodsOf,
  useStepUpMethods,
} from "@slop-factory/auth";
import { Button } from "@slop-factory/ui";
import { apiClient } from "@/api";
import { toLoginProviders } from "@/auth_pages_config";
import { useAuth } from "@/context/auth";
import { errorMessage } from "@/lib/use_async_action";

interface StepUpPromptProps {
  // Where a provider sign-in lands afterwards, for the page to pick its
  // action up again.
  next: string;
  // Called once the password is confirmed. A provider round trip leaves the
  // page instead, so it never calls this.
  onConfirmed: () => void;
  onCancel: () => void;
}

// StepUpPrompt is the "Confirm it's you" prompt a sensitive change opens when
// the server answers reauth_required: the session's last interactive
// sign-in is older than the step-up window. It offers only what the account
// can confirm with.
export default function StepUpPrompt({ next, onConfirmed, onCancel }: StepUpPromptProps) {
  const { providers } = useAuth();
  const configured = useMemo(() => toLoginProviders(providers), [providers]);
  // The shared prompt stays disabled once a password is accepted, expecting
  // the caller to dismiss it; a caller whose retry fails keeps it mounted, so
  // a new key gives the person a fresh prompt to try again from.
  const [attempt, setAttempt] = useState(0);
  const [providerError, setProviderError] = useState<string | null>(null);
  const methods = useStepUpMethods(async () => {
    const body = await apiClient.listSignInMethods();
    return stepUpMethodsOf(
      {
        password: body.password ? { email: body.password.email, verified: body.password.verified } : null,
        identities: body.identities,
        passwordLoginEnabled: body.password_login_enabled,
      },
      configured,
    );
  });

  if (methods.status === "loading") return <p>Loading…</p>;
  if (methods.status === "error") {
    return (
      <div>
        <p role="alert">Failed to load sign-in methods.</p>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    );
  }

  return (
    <>
      {providerError && <p role="alert">{providerError}</p>}
      <SharedStepUpPrompt
        key={attempt}
        copy={defaultSignInMethodsCopy}
        withPassword={methods.withPassword}
        providers={methods.providers}
        describePasswordError={(err) => errorMessage(err, "Failed to confirm.")}
        onPassword={async (password) => {
          await apiClient.reauthWithPassword(password);
          onConfirmed();
          setAttempt((n) => n + 1);
        }}
        // The shared prompt re-enables its buttons on a rejection but shows
        // nothing, so the failure is reported here.
        onProvider={async (provider) => {
          setProviderError(null);
          try {
            redirectTo(await apiClient.startReauth(provider.name, next));
          } catch (err) {
            setProviderError(errorMessage(err, "Failed to start."));
            throw err;
          }
        }}
        onCancel={onCancel}
      />
    </>
  );
}
