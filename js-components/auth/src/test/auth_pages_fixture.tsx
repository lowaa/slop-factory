import type { ReactNode } from "react";
import { fn } from "storybook/test";
import { AuthPagesProvider, type AuthApi, type AuthPagesConfig } from "../config";

// A fake AuthApi for tests, every method resolving by default -- a test
// overrides only the one call it cares about (see e.g. login_page.test.tsx
// spying on signIn's rejection).
export function makeFakeAuthApi(overrides: Partial<AuthApi> = {}): AuthApi {
  return {
    signIn: fn().mockResolvedValue(undefined),
    register: fn().mockResolvedValue(undefined),
    activate: fn().mockResolvedValue(undefined),
    requestPasswordReset: fn().mockResolvedValue(undefined),
    resetPassword: fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

export function makeTestConfig(overrides: Partial<AuthPagesConfig> = {}): AuthPagesConfig {
  return {
    api: makeFakeAuthApi(),
    navigate: fn(),
    routes: { login: "/login", register: "/register", forgotPassword: "/forgot-password" },
    brand: "Test App",
    nameFields: [{ name: "display_name", label: "Display name", autoComplete: "name" }],
    registrationOpen: true,
    describeError: (err) => (err instanceof Error ? err.message : "Something went wrong. Please try again."),
    ...overrides,
  };
}

interface TestAuthPagesProviderProps {
  config?: Partial<AuthPagesConfig>;
  children: ReactNode;
}

// The provider every page/layout test renders under, standing in for
// whichever real app's AuthPagesProvider would normally wrap it.
export function TestAuthPagesProvider({ config, children }: TestAuthPagesProviderProps) {
  return <AuthPagesProvider config={makeTestConfig(config)}>{children}</AuthPagesProvider>;
}
