import { useState } from "react";
import { apiClient, type InvitePreview } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import { useTenants } from "@/lib/shared_resources";
import { useAuth } from "@/context/auth";
import {
  AuthLayout,
  AuthNotice,
  InviteAccept,
  ProviderButtons,
  explainCallbackError,
  explainError,
  readAndClearCallbackError,
  useAuthPages,
} from "@slop-factory/auth";
import { Button } from "@slop-factory/ui";
import { reloadToBoard } from "@/lib/navigation";
import { clearPendingInvite, readToken } from "./pending_invite";
import TenantNameField from "./tenant_name_field";
import { suggestTenantName } from "@/domain/tenant_names";
import { errorMessage } from "@/lib/use_async_action";

interface InvitePageProps {
  onNavigate: (path: string) => void;
}

// finishAndGoToBoard is what every accepting path ends with -- see
// reloadToBoard's own doc comment for why it's a full navigation.
// The page's own words for what a provider round trip can refuse about the
// workspace's name, which the shared auth pages know nothing of.
function explainInviteCallbackError(
  code: string,
  providers: { displayName: string }[],
): string {
  if (code === "tenant_name_taken") {
    return "You already have a workspace with that name.";
  }
  if (code.startsWith("tenant_name_")) {
    return "That isn’t a name a workspace can have.";
  }
  return explainCallbackError(code, providers);
}

function finishAndGoToBoard(): void {
  clearPendingInvite();
  reloadToBoard();
}

// Where accepting through a provider lands: the callback signs the browser
// in, into the invite's tenant.
const AFTER_ACCEPTING = "/";

export default function InvitePage({ onNavigate }: InvitePageProps) {
  const { status: authStatus, profile, signOut } = useAuth();
  const { providers } = useAuthPages();
  const [token] = useState<string | null>(readToken);
  // Back from a provider that refused the invite (/invite?error=<code>):
  // the token went with the first visit -- a full navigation drops
  // pending_invite's module state -- but nothing was spent, so the
  // email's link works again.
  const [callbackError] = useState<string | null>(readAndClearCallbackError);
  // Fetching the preview is idempotent (it just reads the invite, unlike
  // accepting it), so unlike @slop-factory/auth's ActivatePage there is
  // no StrictMode double-invoke guard needed here.
  const fetched = usePolledResource(
    // Hooks cannot be skipped, so a missing token fails here and the page
    // reports its own reason for that below.
    () => (token ? apiClient.previewInvite(token) : Promise.reject(new Error("no invite token"))),
    { deps: [token], fallbackError: "Something went wrong. Please try again." },
  );
  const previewState = token
    ? fetched
    : {
        status: "error" as const,
        data: undefined,
        error: callbackError
          ? `${explainInviteCallbackError(callbackError, providers)} Open the link in your invitation email again to try once more.`
          : "This invite link is missing its token.",
      };

  if (previewState.status === "loading" || authStatus === "loading") {
    return (
      <AuthLayout title="You're invited">
        <p>Loading…</p>
      </AuthLayout>
    );
  }

  if (previewState.status === "error") {
    return (
      <AuthLayout title="You're invited">
        <div className="auth-layout__body">
          <AuthNotice variant="error">{previewState.error}</AuthNotice>
          <p className="auth-layout__switch">
            <a
              href={authStatus === "authenticated" ? "/" : "/login"}
              onClick={(e) => {
                e.preventDefault();
                clearPendingInvite();
                onNavigate(authStatus === "authenticated" ? "/" : "/login");
              }}
            >
              {authStatus === "authenticated" ? "Back to the board" : "Sign in"}
            </a>
          </p>
        </div>
      </AuthLayout>
    );
  }

  const preview = previewState.data;
  if (!preview) return null;
  const workspaceCopy =
    preview.kind === "new_tenant"
      ? "start your own workspace on slop-haus"
      : `join ${preview.tenant_name} on slop-haus`;

  if (!preview.account_exists) {
    return (
      <CreateAccountForm
        token={token!}
        preview={preview}
        workspaceCopy={workspaceCopy}
      />
    );
  }

  if (authStatus === "anonymous") {
    return (
      <AuthLayout title="You're invited" subtitle={preview.email}>
        <div className="auth-layout__body">
          <AuthNotice variant="info">
            {preview.invited_by} invited you to {workspaceCopy}. You already
            have a slop-haus account -- sign in to accept.
          </AuthNotice>
          {preview.kind === "join_tenant" && (
            <ContinueWithProvider token={token!} />
          )}
          <p className="auth-layout__switch">
            <a
              href="/login"
              onClick={(e) => {
                e.preventDefault();
                // pendingToken stays set -- see hasPendingInviteToken --
                // so app.tsx brings the person straight back here once
                // they're signed in.
                onNavigate("/login");
              }}
            >
              Sign in
            </a>
          </p>
        </div>
      </AuthLayout>
    );
  }

  // authStatus === "authenticated" from here.
  if (profile!.email.toLowerCase() !== preview.email.toLowerCase()) {
    return (
      <AuthLayout title="You're invited" subtitle={preview.email}>
        <div className="auth-layout__body">
          <AuthNotice variant="error">
            This invite is for {preview.email}, but you&rsquo;re signed in as{" "}
            {profile!.email}. Sign out to accept it as {preview.email}.
          </AuthNotice>
          <Button
            type="button"
            variant="primary"
            className="auth-layout__submit"
            onClick={() => {
              // The token stays pending: signing out flips authStatus to
              // "anonymous" without unmounting this page, and the
              // "anonymous" branch above then offers signing in as the
              // right address instead.
              void signOut();
            }}
          >
            Sign out
          </Button>
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout title="You're invited" subtitle={preview.email}>
      <div className="auth-layout__body">
        <AuthNotice variant="info">
          {preview.invited_by} invited you to {workspaceCopy}.
        </AuthNotice>
        <AcceptButton
          token={token!}
          newTenant={preview.kind === "new_tenant"}
          displayName={profile!.display_name}
        />
      </div>
    </AuthLayout>
  );
}

function AcceptButton({
  token,
  newTenant,
  displayName,
}: {
  token: string;
  newTenant: boolean;
  displayName: string;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const tenants = useTenants({ enabled: newTenant });
  // An existing account starts from a name that doesn't repeat one of its
  // tenants, which the server would refuse. The field waits for the list,
  // and falls back to no clashes if it fails.
  const [edited, setEdited] = useState<string | null>(null);
  const suggestion = suggestTenantName(displayName, tenants.status === "ready" ? (tenants.data ?? []).map((t) => t.name) : []);
  const tenantName = edited ?? (tenants.status === "loading" ? "" : suggestion);

  async function handleAccept() {
    setError(null);
    setSubmitting(true);
    try {
      await (newTenant
        ? apiClient.acceptInvite(token, tenantName)
        : apiClient.acceptInvite(token));
      finishAndGoToBoard();
    } catch (err) {
      setError(
        errorMessage(err, "Something went wrong. Please try again."),
      );
      setSubmitting(false);
    }
  }

  return (
    <>
      {newTenant && (
        <TenantNameField
          value={tenantName}
          onChange={setEdited}
          disabled={submitting}
          hint="How you tell this workspace from your others."
        />
      )}
      {error && <AuthNotice variant="error">{error}</AuthNotice>}
      <Button
        type="button"
        variant="primary"
        className="auth-layout__submit"
        disabled={submitting || (newTenant && tenantName.trim() === "")}
        onClick={() => void handleAccept()}
      >
        {submitting ? "Accepting…" : "Accept"}
      </Button>
    </>
  );
}

interface CreateAccountFormProps {
  token: string;
  preview: InvitePreview;
  workspaceCopy: string;
}

// With no account yet: a name and a password make one, or a login
// provider does -- the account is then made at the invite's address, with
// the provider's name for it.
function CreateAccountForm({
  token,
  preview,
  workspaceCopy,
}: CreateAccountFormProps) {
  const [displayName, setDisplayName] = useState("");
  const newTenant = preview.kind === "new_tenant";
  // Follows the display name until the person writes their own.
  const [typedTenantName, setTypedTenantName] = useState<string | null>(null);
  const tenantName = typedTenantName ?? suggestTenantName(displayName, []);

  return (
    <AuthLayout title="You're invited" subtitle={preview.email}>
      <InviteAccept
        intro={
          <AuthNotice variant="info">
            {preview.invited_by} invited you to {workspaceCopy}. Choose a name
            and password to finish setting up your account.
          </AuthNotice>
        }
        passwordLabel="Password"
        submitLabel="Create account"
        submittingLabel="Creating account…"
        onAcceptWithPassword={async (password) => {
          await (newTenant
            ? apiClient.acceptInviteAsNewAccount(
                token,
                displayName,
                password,
                tenantName,
              )
            : apiClient.acceptInviteAsNewAccount(token, displayName, password));
          finishAndGoToBoard();
        }}
        // A name left as suggested is the provider's, not the typed display
        // name's, so only a name the person wrote is sent.
        onContinueWithProvider={(provider) =>
          typedTenantName === null || !newTenant
            ? apiClient.startInviteWithProvider(
                provider,
                token,
                AFTER_ACCEPTING,
              )
            : apiClient.startInviteWithProvider(
                provider,
                token,
                AFTER_ACCEPTING,
                typedTenantName,
              )
        }
        sharedFields={
          newTenant && (
            <TenantNameField
              value={tenantName}
              onChange={setTypedTenantName}
              hint="How you tell this workspace from any others you join."
            />
          )
        }
        passwordFields={
          <label className="auth-layout__field">
            <span>Display name</span>
            <input
              type="text"
              autoComplete="name"
              required
              minLength={1}
              maxLength={100}
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
        }
      />
    </AuthLayout>
  );
}

// ContinueWithProvider accepts for an existing account through a login
// provider it already signs in with -- instead of signing in and coming
// back to accept. An identity that isn't the account's is refused, and the
// person lands back here with the reason.
function ContinueWithProvider({ token }: { token: string }) {
  const { providers, copy, describeError, errorCode, redirect } =
    useAuthPages();
  const [error, setError] = useState<string | null>(null);

  return (
    <>
      {error && <AuthNotice variant="error">{error}</AuthNotice>}
      <ProviderButtons
        providers={providers}
        label={copy.continueWithProvider}
        onChoose={async (p) => {
          setError(null);
          try {
            redirect(
              await apiClient.startInviteWithProvider(
                p.name,
                token,
                AFTER_ACCEPTING,
              ),
            );
          } catch (err) {
            setError(
              explainError(err, { errorCode, describeError }, p.displayName),
            );
            throw err;
          }
        }}
      />
    </>
  );
}
