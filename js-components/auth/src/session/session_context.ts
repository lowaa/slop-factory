import { createContext, useContext } from "react";

// "loading" is the gap between mount and the first session probe's answer:
// the session is httpOnly cookies, so there is nothing to read
// synchronously, and every consumer has to be able to render "don't know
// yet" rather than assuming signed-out until proven otherwise.
export type SessionStatus = "loading" | "authenticated" | "anonymous";

// SessionApi is what the provider needs of an app's api client. The
// provider calls these at use time, so an app can hand it thin wrappers
// over its (stubbable) client.
export interface SessionApi<P> {
  // me is the session probe: resolves with the signed-in profile, rejects
  // when there is no session.
  me(): Promise<P>;
  login(email: string, password: string): Promise<P>;
  logout(): Promise<void>;
  // onSignedOut is SessionClient.onSignedOut: the api announcing a session
  // that ended without the person asking.
  onSignedOut(listener: () => void): () => void;
}

export interface SessionContextValue<P, C = undefined> {
  status: SessionStatus;
  // Set only while status is "authenticated".
  profile: P | null;
  // Why the sign-in page is showing up, when the session ended rather than
  // the person signing out. Always null unless the provider was given a
  // sessionEndedNotice. Cleared on the next sign-in.
  notice: string | null;
  // True once this person signs out on purpose, until the next sign-in. The
  // page they were on was theirs: whoever signs in next on this browser may
  // not be who signed out, so an app starts them from its home rather than
  // returning to it. A session that merely ends leaves it false, so signing
  // back in returns to where they were.
  signedOutByUser: boolean;
  // What the provider's loadConfig answered; null without one. Settled
  // before status leaves "loading".
  config: C | null;
  signIn: (email: string, password: string) => Promise<void>;
  // startSession records a session some other route started -- an accepted
  // invite.
  startSession: (profile: P) => void;
  // signOut is the person's own sign-out. A failed logout request never
  // rejects it: signed out here either way, whatever the server says.
  signOut: () => Promise<void>;
}

export const SessionContext = createContext<SessionContextValue<unknown, unknown> | null>(null);

export function useSession<P, C = undefined>(): SessionContextValue<P, C> {
  const ctx = useContext(SessionContext);
  if (!ctx) {
    throw new Error("useSession must be used within a SessionProvider");
  }
  return ctx as unknown as SessionContextValue<P, C>;
}
