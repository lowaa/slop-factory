import { createContext, useContext } from "react";
import type { SessionContextValue } from "@slop-factory/auth";
import type { LoginProviderBody, Profile } from "@/api";

// Split from auth_context.tsx the same way agent_name's provider/hook live
// in separate files (agent_name.tsx / agent_names.ts): a file that exports
// only the provider component keeps react/only-export-components happy for
// fast refresh, and this one holds everything that isn't a component.

export type { SessionStatus as AuthStatus } from "@slop-factory/auth";

// The shared session (status, profile, notice, signedOutByUser, signIn,
// signOut), with the sign-in options GET /api/auth/config offers in place of
// the raw config. AuthProvider holds status at "loading" until both the
// session probe and the config are known, so neither the login nor the
// register page ever has to render before they settle.
export interface AuthContextValue extends Omit<SessionContextValue<Profile>, "config" | "startSession"> {
  // Whether the sign-in page should offer sign-up.
  registrationOpen: boolean;
  // Whether the sign-in page offers an email and password, and which login
  // providers it offers a button for.
  passwordLogin: boolean;
  providers: LoginProviderBody[];
}

export const AuthContext = createContext<AuthContextValue | null>(null);

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return ctx;
}
