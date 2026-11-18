import type { ReactNode } from "react";
import { renderWithRouter, type RenderWithRouterResult } from "@slop-factory/router/testing";
import type { LoginProviderBody, Profile } from "@/api";
import { AuthContext, type AuthContextValue } from "@/context/auth";
import { makeProfile } from "./fixtures";

// signedInAuth is an AuthProvider's value for a session that is already
// signed in as profile, without the provider's own fetches.
export function signedInAuth(
  profile: Profile = makeProfile(),
  providers: LoginProviderBody[] = [],
): AuthContextValue {
  return {
    status: "authenticated",
    profile,
    notice: null,
    signedOutByUser: false,
    registrationOpen: false,
    passwordLogin: true,
    providers,
    signIn: async () => {},
    signOut: async () => {},
  };
}

export interface RenderSignedInOptions {
  profile?: Profile;
  // The sign-in providers the deployment offers.
  providers?: LoginProviderBody[];
  initialPath?: string;
}

// renderSignedIn renders a component the way the signed-in app shows it:
// inside a router (memory history) and a session -- what a component that
// links to a ticket needs, since the link names the session's tenant.
export function renderSignedIn(
  ui: ReactNode,
  { profile, providers, initialPath }: RenderSignedInOptions = {},
): Promise<RenderWithRouterResult> {
  return renderWithRouter(<AuthContext.Provider value={signedInAuth(profile, providers)}>{ui}</AuthContext.Provider>, {
    initialPath,
  });
}
