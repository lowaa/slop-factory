import { fn } from "storybook/test";
import type { IdentityMethod, SignInMethods, SignInMethodsApi } from "../sign_in_methods/types";

// FakeAuthError stands in for an app's API error: a message and the
// server's machine-readable code.
export class FakeAuthError extends Error {
  code: string;

  constructor(code: string, message: string = code) {
    super(message);
    this.name = "FakeAuthError";
    this.code = code;
  }
}

export const fakeErrorCode = (err: unknown): string | undefined =>
  err instanceof FakeAuthError ? err.code : undefined;

export const fakeDescribeError = (err: unknown): string =>
  err instanceof Error ? err.message : "Something went wrong. Please try again.";

export interface FakeServerOptions {
  methods: SignInMethods;
  // The session's sign-in is too old: every change is refused with
  // reauth_required until a step-up.
  stale?: boolean;
  // The account's password, for a step-up with it.
  password?: string;
  // Added passwords wait on an emailed link, as some apps' do.
  addedPasswordsNeedVerifying?: boolean;
  // Where a provider round trip sends the browser.
  providerUrl?: (provider: string, next: string) => string;
}

export type FakeSignInMethodsApi = { [K in keyof SignInMethodsApi]: SignInMethodsApi[K] & ReturnType<typeof fn> };

// makeFakeSignInMethodsApi is an in-memory stand-in for either server's
// sign-in methods API, holding to the same rules -- step-up before any
// change, never the last way in -- so a test or story drives the panel
// through the real refusals rather than canned ones. Every method is a
// spy, to assert what was called.
export function makeFakeSignInMethodsApi(opts: FakeServerOptions): FakeSignInMethodsApi {
  let methods: SignInMethods = structuredClone(opts.methods);
  let stale = opts.stale ?? false;
  let password = opts.password;
  const url = opts.providerUrl ?? ((provider, next) => `https://idp.example.test/${provider}?next=${next}`);

  function steppedUp() {
    if (stale) throw new FakeAuthError("reauth_required", "confirm it's you first");
  }

  function waysIn(m: SignInMethods): number {
    const pw = m.password?.verified && m.passwordLoginEnabled ? 1 : 0;
    return pw + m.identities.filter((i) => i.usable).length;
  }

  return {
    list: fn(async () => structuredClone(methods)),
    addPassword: fn(async ({ email, password: pw }: { email: string; password: string }) => {
      steppedUp();
      if (methods.password) throw new FakeAuthError("password_exists", "this account already has a password");
      methods = { ...methods, password: { email: email || undefined, verified: !opts.addedPasswordsNeedVerifying } };
      password = pw;
    }),
    changePassword: fn(async (pw: string) => {
      steppedUp();
      if (!methods.password) throw new FakeAuthError("not_found", "there is no password to change");
      password = pw;
    }),
    removePassword: fn(async () => {
      steppedUp();
      if (!methods.password) throw new FakeAuthError("not_found", "there is no password");
      const after = { ...methods, password: null };
      if (waysIn(after) === 0) throw new FakeAuthError("last_method", "that is the last way in");
      methods = after;
      password = undefined;
    }),
    removeIdentity: fn(async ({ provider, subject }: { provider: string; subject: string }) => {
      steppedUp();
      const identities = methods.identities.filter((i) => !(i.provider === provider && i.subject === subject));
      if (identities.length === methods.identities.length) throw new FakeAuthError("not_found", "no such identity");
      const after = { ...methods, identities };
      if (waysIn(after) === 0) throw new FakeAuthError("last_method", "that is the last way in");
      methods = after;
    }),
    startLink: fn(async (provider: string, next: string) => {
      steppedUp();
      if (methods.identities.some((i: IdentityMethod) => i.provider === provider)) {
        throw new FakeAuthError("provider_already_linked", "already linked");
      }
      return url(provider, next);
    }),
    startReauth: fn(async (provider: string, next: string) => url(provider, next)),
    reauthWithPassword: fn(async (pw: string) => {
      if (!password || pw !== password) throw new FakeAuthError("invalid_credentials", "invalid email or password");
      stale = false;
    }),
  };
}

export const passwordAndDex: SignInMethods = {
  password: { email: "ada@example.com", verified: true },
  identities: [{ provider: "dex", subject: "sub-ada", login: "ada@example.com", usable: true }],
  passwordLoginEnabled: true,
};

export const passwordOnly: SignInMethods = {
  password: { email: "ada@example.com", verified: true },
  identities: [],
  passwordLoginEnabled: true,
};

export const dexOnly: SignInMethods = {
  password: null,
  identities: [{ provider: "dex", subject: "sub-ada", login: "ada@example.com", usable: true }],
  passwordLoginEnabled: true,
};

export const providers = [
  { name: "dex", displayName: "Dex" },
  { name: "github", displayName: "GitHub" },
];
