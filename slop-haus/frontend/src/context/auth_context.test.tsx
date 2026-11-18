import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { AuthProvider } from "./auth_context";
import { useAuth } from "./auth";
import { apiClient, ApiError } from "@/api";
import { makeProfile, makeTenant } from "@/test/fixtures";
import { defaultResourceClient, resourceKeys } from "@/lib/resource";
import { registerPersonalState } from "@/lib/personal_state";

function wrapper({ children }: { children: ReactNode }) {
  return <AuthProvider>{children}</AuthProvider>;
}

describe("useAuth", () => {
  it("throws when used outside an AuthProvider", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => renderHook(() => useAuth())).toThrow("useAuth must be used within an AuthProvider");
    consoleError.mockRestore();
  });

  // A session from before CSRF tokens can't write, so it is shown sign-in,
  // saying why -- unlike a plain "nobody is signed in".
  it("is anonymous, with the session-ended notice, for a session that can't write", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "sign in again", "csrf"));
    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));
    expect(result.current.notice).toBe("Your session has ended — sign in again.");
  });

  it("has no notice when nobody is signed in", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));
    expect(result.current.notice).toBeNull();
  });

  it("offers the sign-in methods /api/auth/config lists", async () => {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue({
      registration_open: true,
      password_login: false,
      providers: [{ name: "github", display_name: "GitHub" }],
    });
    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => expect(result.current.status).toBe("authenticated"));
    expect(result.current.registrationOpen).toBe(true);
    expect(result.current.passwordLogin).toBe(false);
    expect(result.current.providers).toEqual([{ name: "github", display_name: "GitHub" }]);
  });

  it("falls back to password sign-in with sign-up closed when the config can't be read", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockRejectedValue(new Error("down"));
    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => expect(result.current.status).toBe("anonymous"));
    expect(result.current.registrationOpen).toBe(false);
    expect(result.current.passwordLogin).toBe(true);
    expect(result.current.providers).toEqual([]);
  });
});

// What a person left behind in the browser (a pending invite, unsent
// drafts, remembered filters) is registered by the features that hold it;
// each feature's own test checks that it registers.
describe("signing out", () => {
  it("forgets the registered personal state", async () => {
    const forget = vi.fn();
    registerPersonalState("auth_context.test", { forget });
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("authenticated"));
    expect(forget).not.toHaveBeenCalled();

    await act(() => result.current.signOut());

    expect(forget).toHaveBeenCalled();
  });

  it("empties the server-state cache when the person leaves", async () => {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("authenticated"));
    defaultResourceClient.setQueryData(resourceKeys.tenants(), [makeTenant()]);

    await act(() => result.current.signOut());

    await waitFor(() => expect(defaultResourceClient.getQueryData(resourceKeys.tenants())).toBeUndefined());
  });

  it("binds registered personal state to the signed-in user", async () => {
    const bindUser = vi.fn();
    registerPersonalState("auth_context.test", { forget: () => {}, bindUser });
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ id: "u-mine" }));
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("authenticated"));
    expect(bindUser).toHaveBeenCalledWith("u-mine");
  });
});
