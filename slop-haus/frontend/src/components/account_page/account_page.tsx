import { useState } from "react";
import { ConsentSettings } from "@slop-factory/privacy/consent";
import { SignInMethodsPanel, readAndClearCallbackError, type SignInMethodsApi } from "@slop-factory/auth";
import type { Profile } from "@/api";
import { describeError, errorCode, PASSWORD_RULES, toLoginProviders } from "@/auth_pages_config";
import { useAuth } from "@/context/auth";
import { redirectBrowserTo } from "@/lib/navigation";
import { signInMethodsApi } from "./sign_in_methods_api";
import { ACCOUNT_PATH } from "./account_path";
import "./account_page.scss";

interface AccountPageProps {
  profile: Profile;
  // Overridable for tests; the real client otherwise.
  api?: SignInMethodsApi;
}

// The signed-in person's own things, as opposed to the tenant's (Members,
// Agents): who they are, and how they sign in. A link or step-up through a
// provider comes back here, with ?error=<code> when it was refused.
export default function AccountPage({ profile, api = signInMethodsApi }: AccountPageProps) {
  const { providers } = useAuth();
  const [callbackError] = useState(readAndClearCallbackError);

  return (
    <div className="account-page">
      <h2 className="account-page__title">Account</h2>
      <p className="account-page__who">
        {profile.display_name} <span className="account-page__email">{profile.email}</span>
      </p>
      <SignInMethodsPanel
        api={api}
        providers={toLoginProviders(providers)}
        returnPath={ACCOUNT_PATH}
        callbackError={callbackError}
        passwordRules={PASSWORD_RULES}
        errorCode={errorCode}
        describeError={describeError}
        redirect={redirectBrowserTo}
      />
      <ConsentSettings />
    </div>
  );
}
