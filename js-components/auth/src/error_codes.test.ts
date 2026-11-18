import { afterEach, describe, expect, it } from "vitest";
import { describeAuthCode, explainError, readAndClearCallbackError } from "./error_codes";

describe("readAndClearCallbackError", () => {
  const original = window.location.href;
  afterEach(() => window.history.replaceState(null, "", original));

  it("reads ?error= and scrubs only it from the address bar", () => {
    window.history.replaceState(null, "", "/account?tab=methods&error=identity_in_use#section");

    expect(readAndClearCallbackError()).toBe("identity_in_use");
    expect(window.location.pathname + window.location.search + window.location.hash).toBe("/account?tab=methods#section");
    expect(readAndClearCallbackError()).toBeNull();
  });
});

describe("explainError", () => {
  it("explains a known code in the package's words, naming the provider", () => {
    const err = { code: "identity_in_use" };
    expect(explainError(err, { errorCode: (e) => (e as { code: string }).code, describeError: () => "raw" }, "Dex")).toBe(
      "That Dex account is already linked to a different account here.",
    );
  });

  it("falls back to the app's description for a code it doesn't know, or none", () => {
    expect(explainError({}, { errorCode: () => "something_new", describeError: () => "raw" })).toBe("raw");
    expect(explainError({}, { describeError: () => "raw" })).toBe("raw");
  });

  it("explains each way a sign-up is refused for what the person agreed to", () => {
    expect(describeAuthCode("age_requirement")).toContain("old enough");
    expect(describeAuthCode("signup_consent_required")).toContain("sign-up page");
    expect(describeAuthCode("document_changed")).toContain("has changed");
  });

  it("knows every code the two servers refuse a sign-in method change with", () => {
    for (const code of ["reauth_required", "last_method", "identity_in_use", "provider_already_linked", "identity_mismatch", "session_mismatch", "no_account", "account_exists", "password_exists", "login_expired", "login_cancelled", "provider_unavailable", "provider_error"]) {
      expect(describeAuthCode(code), code).toBeTruthy();
    }
  });
});
