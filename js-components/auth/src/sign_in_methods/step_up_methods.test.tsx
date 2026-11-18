import { describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { stepUpMethodsOf, useStepUpMethods } from "./step_up_methods";
import type { SignInMethods } from "./types";

const configured = [
  { name: "dex", displayName: "Dex" },
  { name: "github", displayName: "GitHub" },
];

const none: SignInMethods = { password: null, identities: [], passwordLoginEnabled: true };

describe("stepUpMethodsOf", () => {
  it("offers only providers the account has a usable identity at", () => {
    const methods: SignInMethods = {
      ...none,
      identities: [
        { provider: "dex", subject: "s", login: "a", usable: true },
        { provider: "github", subject: "t", login: "a", usable: false },
      ],
    };
    expect(stepUpMethodsOf(methods, configured).providers).toEqual([configured[0]]);
  });

  it("offers the password only to an account that has a verified one", () => {
    expect(stepUpMethodsOf(none, configured).withPassword).toBe(false);
    expect(stepUpMethodsOf({ ...none, password: { verified: false } }, configured).withPassword).toBe(false);
    expect(stepUpMethodsOf({ ...none, password: { verified: true } }, configured).withPassword).toBe(true);
    expect(
      stepUpMethodsOf({ ...none, password: { verified: true }, passwordLoginEnabled: false }, configured).withPassword,
    ).toBe(false);
  });
});

describe("useStepUpMethods", () => {
  it("loads, then reports what was loaded", async () => {
    const { result } = renderHook(() => useStepUpMethods(async () => ({ withPassword: true, providers: [configured[0]] })));
    expect(result.current.status).toBe("loading");
    await waitFor(() => expect(result.current).toEqual({ status: "loaded", withPassword: true, providers: [configured[0]] }));
  });

  it("reports a failed load", async () => {
    const { result } = renderHook(() => useStepUpMethods(() => Promise.reject(new Error("no"))));
    await waitFor(() => expect(result.current.status).toBe("error"));
  });
});
