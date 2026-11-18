// What the sign-in methods panel needs from an app's API client. Both
// servers in this repo serve the same shape (GET .../sign-in-methods and
// friends) under their own routes, so each app adapts its client to this.

export interface PasswordMethod {
  // The address the password signs in with, where the app has one per
  // password; absent where the account's own address is it.
  email?: string;
  // Only a verified password signs in; an added one waits on its emailed
  // link.
  verified: boolean;
}

export interface IdentityMethod {
  provider: string;
  subject: string;
  login: string;
  // Whether it can sign in here: only identities of a configured provider
  // can.
  usable: boolean;
}

export interface SignInMethods {
  password: PasswordMethod | null;
  identities: IdentityMethod[];
  passwordLoginEnabled: boolean;
}

// Every change may be refused for want of a recent sign-in -- a rejection
// whose errorCode is "reauth_required" -- and the panel then asks the
// person to confirm it's them and retries.
export interface SignInMethodsApi {
  list(): Promise<SignInMethods>;
  // email is "" unless the panel was told to ask for one (addPasswordAsksEmail).
  addPassword(input: { email: string; password: string }): Promise<void>;
  changePassword(password: string): Promise<void>;
  removePassword(): Promise<void>;
  removeIdentity(identity: { provider: string; subject: string }): Promise<void>;
  // The two round trips through a provider resolve with where to send the
  // browser; it comes back to next.
  startLink(provider: string, next: string): Promise<string>;
  startReauth(provider: string, next: string): Promise<string>;
  reauthWithPassword(password: string): Promise<void>;
}

// A change the panel makes, kept whole so it can be retried after a
// step-up.
export type MethodAction =
  | { kind: "add-password"; email: string; password: string }
  | { kind: "change-password"; password: string }
  | { kind: "remove-password" }
  | { kind: "remove-identity"; provider: string; subject: string }
  | { kind: "link"; provider: string };
