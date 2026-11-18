import type { ReactNode } from "react";
import { AuthPagesProvider, type AuthPagesConfig } from "@slop-factory/auth";

// A minimal, self-contained config -- not this app's real
// useSlopHausAuthPagesConfig, which needs a live AuthProvider (it reads
// signIn/registrationOpen from useAuth()). Tests here don't exercise any
// auth-page behaviour; they just render a screen that uses
// AuthLayout/AuthNotice from @slop-factory/auth and needs *a* provider
// ancestor to do so, same as app.tsx/admin_app.tsx give it for real.
const testConfig: AuthPagesConfig = {
  api: {
    signIn: async () => {},
    register: async () => {},
    activate: async () => {},
    requestPasswordReset: async () => {},
    resetPassword: async () => {},
  },
  navigate: () => {},
  routes: { login: "/login", register: "/register", forgotPassword: "/forgot-password" },
  brand: "slop-haus",
  nameFields: [{ name: "display_name", label: "Display name", autoComplete: "name" }],
  registrationOpen: true,
  describeError: () => "Something went wrong. Please try again.",
};

// config overrides the defaults above -- providers to offer, a redirect
// to watch -- for the test that cares.
export function WithAuthPagesConfig({ children, config }: { children: ReactNode; config?: Partial<AuthPagesConfig> }) {
  return <AuthPagesProvider config={{ ...testConfig, ...config }}>{children}</AuthPagesProvider>;
}
