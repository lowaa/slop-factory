// Wording for the sign-in methods panel and its step-up prompt, overridable
// per app like AuthCopy.
export interface SignInMethodsCopy {
  title: string;
  intro: string;
  loading: string;
  loadFailed: string;
  retry: string;
  password: string;
  passwordNotSet: string;
  passwordUnverified: string;
  passwordLoginOff: string;
  addPassword: string;
  changePassword: string;
  remove: string;
  removePasswordLabel: string;
  removeIdentityLabel: (provider: string) => string;
  confirmRemove: (what: string) => string;
  confirmRemoveButton: string;
  cancel: string;
  save: string;
  saving: string;
  emailLabel: string;
  newPasswordLabel: string;
  identityUnusable: string;
  linkProvider: (provider: string) => string;
  passwordAdded: string;
  passwordAddedUnverified: (email: string) => string;
  passwordChanged: string;
  passwordRemoved: string;
  identityRemoved: (provider: string) => string;
  stepUpTitle: string;
  stepUpIntro: string;
  stepUpPasswordLabel: string;
  stepUpConfirm: string;
  stepUpConfirming: string;
  stepUpWrongPassword: string;
  stepUpNoWay: string;
  continueWithProvider: (provider: string) => string;
  resumed: string;
  resumeRetypePassword: string;
  resumeContinue: string;
}

export const defaultSignInMethodsCopy: SignInMethodsCopy = {
  title: "Sign-in methods",
  intro: "The ways you can sign in to this account.",
  loading: "Loading…",
  loadFailed: "Couldn’t load your sign-in methods.",
  retry: "Try again",
  password: "Password",
  passwordNotSet: "Not set",
  passwordUnverified: "waiting for you to confirm the email address",
  passwordLoginOff: "Password sign-in is turned off here.",
  addPassword: "Add a password",
  changePassword: "Change password",
  remove: "Remove",
  removePasswordLabel: "Remove password",
  removeIdentityLabel: (provider) => `Remove ${provider} sign-in`,
  confirmRemove: (what) => `Remove ${what}? You won’t be able to sign in with it any more.`,
  confirmRemoveButton: "Yes, remove",
  cancel: "Cancel",
  save: "Save",
  saving: "Saving…",
  emailLabel: "Email",
  newPasswordLabel: "New password",
  identityUnusable: "not available here",
  linkProvider: (provider) => `Link ${provider}`,
  passwordAdded: "Password added.",
  passwordAddedUnverified: (email) =>
    `Check ${email} for a link to confirm it. The password works once it’s confirmed.`,
  passwordChanged: "Password changed. Any other sessions have been signed out.",
  passwordRemoved: "Password removed.",
  identityRemoved: (provider) => `${provider} sign-in removed.`,
  stepUpTitle: "Confirm it’s you",
  stepUpIntro: "You signed in a while ago. Confirm it’s you to make this change.",
  stepUpPasswordLabel: "Current password",
  stepUpConfirm: "Confirm",
  stepUpConfirming: "Confirming…",
  stepUpWrongPassword: "That password isn’t right.",
  stepUpNoWay: "Sign out and sign in again, then make the change.",
  continueWithProvider: (provider) => `Continue with ${provider}`,
  resumed: "Thanks — you’re confirmed.",
  resumeRetypePassword: "Thanks — you’re confirmed. Enter the password again to finish.",
  resumeContinue: "Continue",
};
