import { createContext, useContext, type ComponentType, type ReactNode } from "react";
import { redirectTo } from "./navigation";

// What the pages need from an app's API client. Each app adapts its own
// client to this shape rather than the pages knowing any one API's routes
// or request bodies.
export interface AuthApi {
  signIn(email: string, password: string): Promise<void>;
  register(input: RegisterInput): Promise<void>;
  activate(token: string): Promise<void>;
  requestPasswordReset(email: string): Promise<void>;
  resetPassword(token: string, password: string): Promise<void>;
  // Starts signing up with a provider, resolving with where to send the
  // browser -- a POST rather than a link, so what signUpExtras collected goes
  // in its body, where a link someone else made cannot put it. Absent, the
  // register page offers no provider buttons.
  startProviderSignUp?(provider: string, extras: ExtraValues): Promise<string>;
}

export interface RegisterInput {
  email: string;
  password: string;
  // Keyed by NameField.name.
  names: Record<string, string>;
  // What SignUpExtras collected; absent when the app configures none.
  extras?: ExtraValues;
}

// Values an app's own sign-up inputs hold, keyed however it likes. The
// pages only carry them from its fields to its API calls.
export type ExtraValues = Record<string, unknown>;

// Inputs an app adds to its sign-up forms (the register page and the invite
// page) -- agreement to a notice, say. The pages render Fields, keep the
// values, hold submit and the provider buttons back until isComplete says
// they are enough, and hand the values to the app's calls.
export interface SignUpExtras {
  Fields: ComponentType<{ values: ExtraValues; onChange: (values: ExtraValues) => void; disabled?: boolean }>;
  isComplete: (values: ExtraValues) => boolean;
}

// The name inputs the register form asks for, in order -- a display name
// for one app, first and last name for another. minLength/maxLength are
// optional because not every app constrains its name inputs client-side.
export interface NameField {
  name: string;
  label: string;
  autoComplete: string;
  minLength?: number;
  maxLength?: number;
}

// A login provider (GitHub, an OIDC issuer) an app lets people sign in
// with besides a password. name is what the app's routes and identities
// know it by; displayName is what a person is shown ("Continue with Dex").
export interface LoginProvider {
  name: string;
  displayName: string;
}

export interface AuthRoutes {
  login: string;
  register: string;
  forgotPassword: string;
}

export interface PasswordRules {
  minLength?: number;
  maxLength?: number;
}

export type AuthAction = "sign-in" | "register" | "activate" | "request-password-reset" | "reset-password";

// Wording that differs between apps. Messages that echo what the person
// typed take it as an argument; whether to echo it at all is the app's
// call, since echoing an address can confirm it has an account.
export interface AuthCopy {
  signIn: string;
  signingIn: string;
  backToSignIn: string;
  noAccountPrompt: string;
  signUpLink: string;
  // The register page's own "already have an account" prompt -- distinct
  // from noAccountPrompt (the login page's converse of it) since an app
  // can word the two differently.
  alreadyHaveAccountPrompt: string;
  registerTitle: string;
  createAccount: string;
  creatingAccount: string;
  registrationClosedTitle: string;
  registrationClosed: ReactNode;
  registerSent: (email: string) => ReactNode;
  activateTitle: string;
  activating: string;
  activated: string;
  forgotPasswordLink: string;
  forgotPasswordTitle: string;
  sendResetLink: string;
  sendingResetLink: string;
  resetLinkSent: (email: string) => ReactNode;
  resetPasswordTitle: string;
  newPasswordLabel: string;
  setNewPassword: string;
  settingNewPassword: string;
  passwordChanged: string;
  continueToSignIn: string;
  missingActivationToken: string;
  missingResetToken: string;
  continueWithProvider: (displayName: string) => string;
  // Between the provider buttons and the password form.
  orDivider: string;
}

export const defaultCopy: AuthCopy = {
  signIn: "Sign in",
  signingIn: "Signing in…",
  backToSignIn: "Back to sign in",
  noAccountPrompt: "Don’t have an account?",
  signUpLink: "Create one",
  alreadyHaveAccountPrompt: "Already have an account?",
  registerTitle: "Create an account",
  createAccount: "Create account",
  creatingAccount: "Creating account…",
  registrationClosedTitle: "Sign-up is by invitation",
  registrationClosed: "This site doesn’t take open sign-ups. Ask whoever runs it to send you an invite.",
  registerSent: (email) => (
    <>
      If that address can be registered, we&rsquo;ve sent <strong>{email}</strong> a link to activate the account.
    </>
  ),
  activateTitle: "Activate your account",
  activating: "Activating…",
  activated: "Your account is active. You can now sign in.",
  forgotPasswordLink: "Forgot your password?",
  forgotPasswordTitle: "Reset your password",
  sendResetLink: "Send reset link",
  sendingResetLink: "Sending…",
  resetLinkSent: () => "If that address has an account, a reset link is on its way.",
  resetPasswordTitle: "Set a new password",
  newPasswordLabel: "New password",
  setNewPassword: "Set new password",
  settingNewPassword: "Saving…",
  passwordChanged: "Your password has been changed. You can now sign in.",
  continueToSignIn: "Sign in",
  missingActivationToken: "This activation link is missing its token.",
  missingResetToken: "This password reset link is missing its token.",
  continueWithProvider: (displayName) => `Continue with ${displayName}`,
  orDivider: "or",
};

export interface AuthPagesConfig {
  api: AuthApi;
  navigate: (path: string) => void;
  routes: AuthRoutes;
  // Shown at the top of every auth card.
  brand: ReactNode;
  // Added to AuthLayout's root element, so an app can restyle the card
  // under its own class (`.my-app-auth .auth-layout__card`) and win over
  // the package's single-class rules whatever order the stylesheets load
  // in.
  layoutClassName?: string;
  nameFields: NameField[];
  // False hides the sign-up link and turns the register page into a
  // "by invitation" notice instead of a form that can only fail.
  registrationOpen: boolean;
  passwordRules?: PasswordRules;
  copy?: Partial<AuthCopy>;
  // The message to show for a failed call: the app knows its own error
  // type and which server messages are safe to show as-is.
  describeError: (err: unknown) => string;
  onError?: (action: AuthAction, err: unknown) => void;
  // The machine-readable code on a failed call (reauth_required,
  // identity_in_use, ...), when the app's error carries one. A code this
  // package knows is explained in its own words (see error_codes.ts);
  // anything else falls back to describeError.
  errorCode?: (err: unknown) => string | undefined;
  // Extra inputs on the sign-up forms; absent, they look as they always did.
  signUpExtras?: SignUpExtras;
  // Login providers to offer beside the password form, in order. Empty or
  // absent offers none, and the pages look exactly as they did before
  // providers existed.
  providers?: LoginProvider[];
  // Where a plain provider sign-in starts: a URL the browser navigates to
  // (a GET, since it only ever signs in as whoever the browser is at the
  // provider). Needed when providers is non-empty.
  providerSignInUrl?: (provider: string) => string;
  // False hides the password form -- a deployment that signs in only
  // through providers. Defaults to true.
  passwordLogin?: boolean;
  // False hides "forgot your password?" -- a deployment with no email to
  // send a reset link through. Defaults to true.
  passwordReset?: boolean;
  // Extra content under the sign-in page's error, chosen by the error's
  // code (a code from errorCode, or the one a refused provider callback came
  // back with): a way forward the page cannot know about itself, such as
  // cancelling a deletion the account is scheduled for.
  signInErrorExtra?: (code: string | undefined) => ReactNode;
  // A full-page navigation, to a provider or a URL a server answered with.
  // Defaults to window.location.assign; overridable so a test or story can
  // watch it rather than leave the page.
  redirect?: (url: string) => void;
}

type Defaulted = "copy" | "providers" | "passwordLogin" | "passwordReset" | "redirect";

interface ResolvedConfig extends Omit<AuthPagesConfig, Defaulted> {
  copy: AuthCopy;
  providers: LoginProvider[];
  passwordLogin: boolean;
  passwordReset: boolean;
  redirect: (url: string) => void;
}

const AuthPagesContext = createContext<ResolvedConfig | null>(null);

export function AuthPagesProvider({ config, children }: { config: AuthPagesConfig; children: ReactNode }) {
  const resolved: ResolvedConfig = {
    ...config,
    copy: { ...defaultCopy, ...config.copy },
    providers: config.providers ?? [],
    passwordLogin: config.passwordLogin ?? true,
    passwordReset: config.passwordReset ?? true,
    redirect: config.redirect ?? redirectTo,
  };
  return <AuthPagesContext.Provider value={resolved}>{children}</AuthPagesContext.Provider>;
}

export function useAuthPages(): ResolvedConfig {
  const config = useContext(AuthPagesContext);
  if (!config) {
    throw new Error("auth pages rendered outside an AuthPagesProvider");
  }
  return config;
}
