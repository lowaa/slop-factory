import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { SessionContext, type SessionApi, type SessionContextValue, type SessionStatus } from "./session_context";

export interface SessionProviderProps<P, C> {
  api: SessionApi<P>;
  // loadConfig runs beside the session probe, and the provider holds
  // "loading" until both are known. It should resolve with a fallback
  // rather than reject; a rejection leaves config null.
  loadConfig?: () => Promise<C>;
  // The notice shown when a session ends on its own, or the probe finds one
  // that probeEndedSession says had ended.
  sessionEndedNotice?: string;
  // Whether a failed probe was a session ending, as opposed to nobody
  // being signed in.
  probeEndedSession?: (err: unknown) => boolean;
  // onSignOut clears the app's own state that belonged to the person
  // signing out. Runs on a deliberate sign-out only: a session that merely
  // expires keeps it, so signing back in returns to the same flow.
  onSignOut?: () => void;
  children: ReactNode;
}

// SessionProvider owns the state every route branches on: is there a
// signed-in session, and whose.
export function SessionProvider<P, C = undefined>({
  api,
  loadConfig,
  sessionEndedNotice,
  probeEndedSession,
  onSignOut,
  children,
}: SessionProviderProps<P, C>) {
  const [status, setStatus] = useState<SessionStatus>("loading");
  const [profile, setProfile] = useState<P | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [signedOutByUser, setSignedOutByUser] = useState(false);
  const [config, setConfig] = useState<C | null>(null);

  // Read at call time, so a caller passing fresh closures each render
  // neither re-runs the probe nor leaves it holding stale ones.
  const options = useRef({ loadConfig, sessionEndedNotice, probeEndedSession, onSignOut });
  useEffect(() => {
    options.current = { loadConfig, sessionEndedNotice, probeEndedSession, onSignOut };
  });
  // Mirrors signedOutByUser for the onSignedOut listener, which outlives
  // any render.
  const byUser = useRef(false);
  // Counts changes the probe didn't make. A probe still out when the person
  // signs in (an accepted invite), signs out or is told the session ended
  // answers about a session that is no longer the current one, and must not
  // overwrite what happened since.
  const changes = useRef(0);

  useEffect(() => {
    let cancelled = false;
    const startedAt = changes.current;
    const superseded = () => cancelled || changes.current !== startedAt;
    // Started with the probe rather than after it, so the two independent
    // requests run concurrently.
    const configPromise: Promise<C | null> = (options.current.loadConfig?.() ?? Promise.resolve(null)).catch(
      () => null,
    );
    api
      .me()
      .then(async (p) => {
        const loaded = await configPromise;
        if (superseded()) return;
        setConfig(loaded);
        setProfile(p);
        setStatus("authenticated");
      })
      .catch(async (err: unknown) => {
        const loaded = await configPromise;
        if (superseded()) return;
        setConfig(loaded);
        setStatus("anonymous");
        if (options.current.probeEndedSession?.(err)) setNotice(options.current.sessionEndedNotice ?? null);
      });
    return () => {
      cancelled = true;
    };
  }, [api]);

  // Registered once for the app's lifetime rather than per page: which page
  // is on screen when a session ends is exactly what the api doesn't know.
  useEffect(() => {
    return api.onSignedOut(() => {
      // A request still in flight when the person signed out fails once
      // they have, and that mustn't pass for their session expiring.
      if (byUser.current) return;
      changes.current++;
      setProfile(null);
      setStatus("anonymous");
      setNotice(options.current.sessionEndedNotice ?? null);
    });
  }, [api]);

  const startSession = useCallback((p: P) => {
    byUser.current = false;
    changes.current++;
    setSignedOutByUser(false);
    setNotice(null);
    setProfile(p);
    setStatus("authenticated");
  }, []);

  const signIn = useCallback(
    async (email: string, password: string) => {
      startSession(await api.login(email, password));
    },
    [api, startSession],
  );

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } catch {
      // Signed out here either way: the cookies are httpOnly, so clearing
      // them is the server's to do, and a failed request leaves at worst a
      // session this page no longer uses. Callers fire this with `void`, so
      // rethrowing would only surface as an unhandled rejection that global
      // error handlers report as a frontend fault.
    } finally {
      byUser.current = true;
      changes.current++;
      try {
        options.current.onSignOut?.();
      } finally {
        setProfile(null);
        setStatus("anonymous");
        setNotice(null);
        setSignedOutByUser(true);
      }
    }
  }, [api]);

  const value = useMemo<SessionContextValue<P, C>>(
    () => ({ status, profile, notice, signedOutByUser, config, signIn, startSession, signOut }),
    [status, profile, notice, signedOutByUser, config, signIn, startSession, signOut],
  );

  return <SessionContext.Provider value={value as unknown as SessionContextValue<unknown, unknown>}>{children}</SessionContext.Provider>;
}
