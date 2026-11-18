import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { SessionProvider, type SessionProviderProps } from "./session_provider";
import { useSession, type SessionApi } from "./session_context";

interface Profile {
  id: string;
}
const ADA: Profile = { id: "ada" };
const ENDED = "Your session has ended.";

// A fake decides nothing: each method answers what the test set up, and
// announce() is the api telling the app a session ended.
class FakeApi implements SessionApi<Profile> {
  me = vi.fn<() => Promise<Profile>>(() => Promise.reject(new Error("no session")));
  login = vi.fn<(email: string, password: string) => Promise<Profile>>(() => Promise.resolve(ADA));
  logout = vi.fn<() => Promise<void>>(() => Promise.resolve());
  unsubscribe = vi.fn();
  subscribed = vi.fn();
  private listener: (() => void) | null = null;

  onSignedOut(listener: () => void): () => void {
    this.listener = listener;
    this.subscribed();
    return () => {
      this.listener = null;
      this.unsubscribe();
    };
  }
  hasListener(): boolean {
    return this.listener !== null;
  }
  announce(): void {
    this.listener?.();
  }
}

function render<C = undefined>(api: FakeApi, props: Partial<SessionProviderProps<Profile, C>> = {}) {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <SessionProvider api={api} {...props}>
      {children}
    </SessionProvider>
  );
  return renderHook(() => useSession<Profile, C>(), { wrapper });
}

async function signedInSession(api: FakeApi, props: Partial<SessionProviderProps<Profile, undefined>> = {}) {
  api.me.mockResolvedValue(ADA);
  const hook = render(api, props);
  await waitFor(() => expect(hook.result.current.status).toBe("authenticated"));
  return hook;
}

describe("useSession", () => {
  it("throws outside a SessionProvider", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => renderHook(() => useSession())).toThrow("useSession must be used within a SessionProvider");
    consoleError.mockRestore();
  });
});

describe("session probe", () => {
  it("starts loading, then authenticated with the profile", async () => {
    const api = new FakeApi();
    api.me.mockResolvedValue(ADA);
    const { result } = render(api);
    expect(result.current.status).toBe("loading");

    await waitFor(() => expect(result.current.status).toBe("authenticated"));

    expect(api.me).toHaveBeenCalledTimes(1);
    expect(result.current.profile).toEqual(ADA);
  });

  it("starts loading, then anonymous with no notice when there is no session", async () => {
    const { result } = render(new FakeApi(), { sessionEndedNotice: ENDED, probeEndedSession: () => false });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    expect(result.current.profile).toBeNull();
    expect(result.current.notice).toBeNull();
  });

  it("shows the notice when the probe finds a session that had ended", async () => {
    const { result } = render(new FakeApi(), { sessionEndedNotice: ENDED, probeEndedSession: () => true });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    expect(result.current.notice).toBe(ENDED);
  });

  // The probe re-runs when the api changes, the first run cancelled: its
  // late answer must not overwrite the one that is current.
  it("ignores a cancelled probe's late answer", async () => {
    const first = new FakeApi();
    const second = new FakeApi();
    let answerFirst!: (p: Profile) => void;
    first.me.mockReturnValue(new Promise<Profile>((resolve) => (answerFirst = resolve)));
    second.me.mockResolvedValue({ id: "current" });
    let api = first;
    const wrapper = ({ children }: { children: ReactNode }) => <SessionProvider api={api}>{children}</SessionProvider>;
    const { result, rerender } = renderHook(() => useSession<Profile>(), { wrapper });
    api = second;
    rerender();
    await waitFor(() => expect(result.current.profile).toEqual({ id: "current" }));

    await act(async () => answerFirst({ id: "stale" }));

    expect(result.current.profile).toEqual({ id: "current" });
  });

  // An accepted invite starts a session while the page's first probe is
  // still out; the probe's "nobody is signed in" is about the session before.
  it("does not overwrite a session started while the probe is out", async () => {
    const api = new FakeApi();
    let rejectProbe!: (e: Error) => void;
    api.me.mockReturnValue(new Promise<Profile>((_, reject) => (rejectProbe = reject)));
    const { result } = render(api);

    act(() => result.current.startSession(ADA));
    await act(async () => rejectProbe(new Error("no session")));

    expect(result.current.status).toBe("authenticated");
    expect(result.current.profile).toEqual(ADA);
  });

  it("does not sign back in a probe that answers after the person signed out", async () => {
    const api = new FakeApi();
    let answerProbe!: (p: Profile) => void;
    api.me.mockReturnValue(new Promise<Profile>((resolve) => (answerProbe = resolve)));
    const { result } = render(api);

    await act(() => result.current.signOut());
    await act(async () => answerProbe(ADA));

    expect(result.current.status).toBe("anonymous");
    expect(result.current.profile).toBeNull();
  });
});

describe("config", () => {
  it("holds loading until the config and the probe are both known", async () => {
    const api = new FakeApi();
    api.me.mockResolvedValue(ADA);
    let settle!: (c: { open: boolean }) => void;
    const loadConfig = vi.fn(() => new Promise<{ open: boolean }>((resolve) => (settle = resolve)));
    const { result } = render(api, { loadConfig });

    await waitFor(() => expect(api.me).toHaveBeenCalled());
    expect(loadConfig).toHaveBeenCalledTimes(1);
    expect(result.current.status).toBe("loading");
    expect(result.current.config).toBeNull();

    await act(async () => settle({ open: true }));

    await waitFor(() => expect(result.current.status).toBe("authenticated"));
    expect(result.current.config).toEqual({ open: true });
  });

  it("settles anonymous with the config when there is no session", async () => {
    const { result } = render(new FakeApi(), { loadConfig: () => Promise.resolve({ open: false }) });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    expect(result.current.config).toEqual({ open: false });
  });

  it("settles with no config when the loader rejects", async () => {
    const { result } = render(new FakeApi(), { loadConfig: () => Promise.reject(new Error("down")) });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    expect(result.current.config).toBeNull();
  });
});

describe("signIn", () => {
  it("logs in with the credentials and becomes authenticated", async () => {
    const api = new FakeApi();
    const { result } = render(api);
    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    await act(() => result.current.signIn("a@example.com", "pw"));

    expect(api.login).toHaveBeenCalledWith("a@example.com", "pw");
    expect(result.current.status).toBe("authenticated");
    expect(result.current.profile).toEqual(ADA);
  });

  it("stays anonymous and rethrows when the login fails", async () => {
    const api = new FakeApi();
    api.login.mockRejectedValue(new Error("invalid email or password"));
    const { result } = render(api);
    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    await expect(act(() => result.current.signIn("a@example.com", "wrong"))).rejects.toThrow(
      "invalid email or password",
    );

    expect(result.current.status).toBe("anonymous");
  });

  it("clears the notice and signedOutByUser", async () => {
    const api = new FakeApi();
    const { result } = await signedInSession(api, { sessionEndedNotice: ENDED });
    act(() => api.announce());
    expect(result.current.notice).toBe(ENDED);

    await act(() => result.current.signIn("a@example.com", "pw"));
    expect(result.current.notice).toBeNull();

    await act(() => result.current.signOut());
    expect(result.current.signedOutByUser).toBe(true);
    await act(() => result.current.signIn("a@example.com", "pw"));
    expect(result.current.signedOutByUser).toBe(false);
  });
});

describe("startSession", () => {
  it("records a session some other route started", async () => {
    const { result } = render(new FakeApi());
    await waitFor(() => expect(result.current.status).toBe("anonymous"));

    act(() => result.current.startSession(ADA));

    expect(result.current.status).toBe("authenticated");
    expect(result.current.profile).toEqual(ADA);
  });
});

describe("signOut", () => {
  it("logs out, clears the session and marks it the person's own", async () => {
    const api = new FakeApi();
    const { result } = await signedInSession(api, { sessionEndedNotice: ENDED });

    await act(() => result.current.signOut());

    expect(api.logout).toHaveBeenCalledTimes(1);
    expect(result.current.status).toBe("anonymous");
    expect(result.current.profile).toBeNull();
    expect(result.current.notice).toBeNull();
    expect(result.current.signedOutByUser).toBe(true);
  });

  it("runs onSignOut", async () => {
    const onSignOut = vi.fn();
    const { result } = await signedInSession(new FakeApi(), { onSignOut });

    await act(() => result.current.signOut());

    expect(onSignOut).toHaveBeenCalledTimes(1);
  });

  it("signs out, without an unhandled rejection, when the logout request fails", async () => {
    const api = new FakeApi();
    api.logout.mockRejectedValue(new Error("network down"));
    const onSignOut = vi.fn();
    const { result } = await signedInSession(api, { onSignOut });

    const unhandled: unknown[] = [];
    const onUnhandled = (e: PromiseRejectionEvent) => unhandled.push(e.reason);
    window.addEventListener("unhandledrejection", onUnhandled);
    const onProcessUnhandled = (reason: unknown) => unhandled.push(reason);
    process.on("unhandledRejection", onProcessUnhandled);
    try {
      await act(async () => {
        void result.current.signOut();
      });
      // Unhandled rejections are reported after the microtask queue drains.
      await new Promise((resolve) => setTimeout(resolve, 0));
    } finally {
      window.removeEventListener("unhandledrejection", onUnhandled);
      process.off("unhandledRejection", onProcessUnhandled);
    }

    expect(result.current.status).toBe("anonymous");
    expect(result.current.profile).toBeNull();
    expect(result.current.signedOutByUser).toBe(true);
    expect(onSignOut).toHaveBeenCalledTimes(1);
    expect(unhandled).toEqual([]);
  });

  // A request still in flight when the person signs out fails once they
  // have -- the api announces the session over, possibly after the logout
  // answered -- and that mustn't pass for the session expiring.
  it("stays signed out by the person when a request fails around their sign-out", async () => {
    const api = new FakeApi();
    const { result } = await signedInSession(api, { sessionEndedNotice: ENDED });
    let logoutAnswered!: () => void;
    api.logout.mockReturnValue(new Promise<void>((resolve) => (logoutAnswered = resolve)));

    let signedOut!: Promise<void>;
    act(() => {
      signedOut = result.current.signOut();
    });
    act(() => api.announce());
    await act(async () => {
      logoutAnswered();
      await signedOut;
    });
    act(() => api.announce());

    expect(result.current.status).toBe("anonymous");
    expect(result.current.signedOutByUser).toBe(true);
    expect(result.current.notice).toBeNull();
  });
});

describe("onSignedOut", () => {
  it("drops to anonymous with the notice, leaving signedOutByUser false", async () => {
    const api = new FakeApi();
    const onSignOut = vi.fn();
    const { result } = await signedInSession(api, { sessionEndedNotice: ENDED, onSignOut });

    act(() => api.announce());

    expect(result.current.status).toBe("anonymous");
    expect(result.current.profile).toBeNull();
    expect(result.current.notice).toBe(ENDED);
    expect(result.current.signedOutByUser).toBe(false);
    expect(onSignOut).not.toHaveBeenCalled();
  });

  it("subscribes once for the provider's lifetime and unsubscribes on unmount", async () => {
    const api = new FakeApi();
    const { result, rerender, unmount } = render(api);
    await waitFor(() => expect(result.current.status).toBe("anonymous"));
    rerender();
    expect(api.subscribed).toHaveBeenCalledTimes(1);

    unmount();

    expect(api.unsubscribe).toHaveBeenCalledTimes(1);
    expect(api.hasListener()).toBe(false);
  });
});
