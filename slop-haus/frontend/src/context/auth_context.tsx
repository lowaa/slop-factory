import { useEffect, useMemo, useRef, type ReactNode } from "react";
import { SessionProvider, useSession, type SessionApi } from "@slop-factory/auth";
import { apiClient, ApiError, onSignedOut, type AuthConfig, type Profile } from "@/api";
import { AuthContext, type AuthContextValue } from "./auth";
import { bindPersonalStateUser, forgetPersonalState } from "@/lib/personal_state";
import { defaultResourceClient } from "@/lib/resource";

const SESSION_ENDED = "Your session has ended — sign in again.";

// What the sign-in page offers when /api/auth/config can't be read: sign-up
// closed (hiding a link that should have been there is a much smaller
// mistake than showing one that leads to a 403), the password form -- the
// way in every account had before providers -- and no provider buttons.
const FALLBACK_AUTH_CONFIG: AuthConfig = { registration_open: false, password_login: true, providers: [] };

// Calls through apiClient at use time, so tests can stub it.
const sessionApi: SessionApi<Profile> = {
  me: () => apiClient.me(),
  login: (email, password) => apiClient.login(email, password),
  logout: () => apiClient.logout(),
  onSignedOut: (listener) => onSignedOut(listener),
};

const loadAuthConfig = () => apiClient.getAuthConfig().catch(() => FALLBACK_AUTH_CONFIG);

// me() uses the "probe" session mode, so a 401 is just the answer to "is
// anyone signed in" -- not a session ending, so no notice. The exception is
// a session from before CSRF tokens, which is there but can't write: that
// one did end.
const probeEndedSession = (err: unknown) => err instanceof ApiError && err.code === "csrf";

// Signing out forgets what features registered as personal state (see
// lib/personal_state.ts): on a shared browser the next to sign in must not
// walk into the last person's invite, drafts or filters. A session that
// merely expires keeps them -- signing back in should return to the same flow.
export function AuthProvider({ children }: { children: ReactNode }) {
  return (
    <SessionProvider
      api={sessionApi}
      loadConfig={loadAuthConfig}
      sessionEndedNotice={SESSION_ENDED}
      probeEndedSession={probeEndedSession}
      onSignOut={forgetPersonalState}
    >
      <AuthBridge>{children}</AuthBridge>
    </SessionProvider>
  );
}

// AuthBridge gives the app its AuthContext from the shared session plus the
// sign-in options /api/auth/config offered.
function AuthBridge({ children }: { children: ReactNode }) {
  const session = useSession<Profile, AuthConfig>();
  // During render rather than in an effect: the page below reads its personal state
  // as it first renders, before an effect here would have run.
  const userId = session.profile?.id;
  useMemo(() => {
    if (userId) bindPersonalStateUser(userId);
  }, [userId]);
  // Whatever the cache holds was read as the person who just left, whether
  // they signed out or the session expired; the next to sign in must not be
  // answered from it.
  const lastUserId = useRef(userId);
  useEffect(() => {
    if (lastUserId.current !== undefined && lastUserId.current !== userId) defaultResourceClient.clear();
    lastUserId.current = userId;
  }, [userId]);
  const value = useMemo<AuthContextValue>(() => {
    const authConfig = session.config ?? FALLBACK_AUTH_CONFIG;
    return {
      status: session.status,
      profile: session.profile,
      notice: session.notice,
      signedOutByUser: session.signedOutByUser,
      signIn: session.signIn,
      signOut: session.signOut,
      registrationOpen: authConfig.registration_open,
      passwordLogin: authConfig.password_login,
      providers: authConfig.providers,
    };
  }, [session]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
