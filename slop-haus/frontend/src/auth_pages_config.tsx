import { useMemo } from "react";
import type { AuthPagesConfig, AuthRoutes, LoginProvider } from "@slop-factory/auth";
import { apiClient, ApiError, type LoginProviderBody } from "@/api";
import { useAuth } from "@/context/auth";
import { redirectBrowserTo } from "@/lib/navigation";
import { providerReturnPath } from "@/auth_routes";
import { BrandLogo } from "@/components/brand_logo";
import "./auth_pages_config.scss";
import { errorMessage } from "@/lib/use_async_action";

// The server's rule (user/service.go): 10 characters to bcrypt's 72 bytes.
export const PASSWORD_RULES = { minLength: 10, maxLength: 72 };

export function describeError(err: unknown): string {
  return errorMessage(err, "Something went wrong. Please try again.");
}

export function errorCode(err: unknown): string | undefined {
  return err instanceof ApiError ? err.code : undefined;
}

export function toLoginProviders(providers: LoginProviderBody[]): LoginProvider[] {
  return providers.map((p) => ({ name: p.name, displayName: p.display_name }));
}

// How the auth pages move between each other: the main SPA's router
// (routes.tsx), or a full page load from the admin bundle.
export interface AuthPagesNavigation {
  navigate: (path: string) => void;
  routes: AuthRoutes;
}

// Adapts this app's apiClient/useAuth to @slop-factory/auth's generic
// config shape. A hook (not a plain object) because it needs useAuth() --
// both the main SPA (routes.tsx's RootLayout) and the admin bundle
// (admin_app.tsx) call it, each wrapping its own subtree in
// AuthPagesProvider so AuthLayout (shared with invite/authorize/device and
// the admin sign-in prompt) can read its brand from context wherever it's
// rendered, not just from the five auth pages themselves. Memoized on what
// it reads from useAuth() and on navigation, so AuthPagesProvider's context
// value stays referentially stable across renders that change none of
// them -- navigation has to be a stable reference itself.
export function useSlopHausAuthPagesConfig(navigation: AuthPagesNavigation): AuthPagesConfig {
  const { signIn, registrationOpen, passwordLogin, providers } = useAuth();

  return useMemo<AuthPagesConfig>(
    () => ({
      api: {
        signIn,
        register: (input) => apiClient.register(input.email, input.password, input.names.display_name ?? ""),
        activate: (token) => apiClient.activate(token),
        requestPasswordReset: (email) => apiClient.requestPasswordReset(email),
        resetPassword: (token, password) => apiClient.resetPassword(token, password),
      },
      navigate: navigation.navigate,
      routes: navigation.routes,
      brand: (
        <p className="slop-haus-auth-brand">
          <BrandLogo size={64} />
          <span>slop-haus</span>
        </p>
      ),
      layoutClassName: "slop-haus-auth",
      nameFields: [{ name: "display_name", label: "Display name", autoComplete: "name", minLength: 1, maxLength: 100 }],
      registrationOpen,
      passwordRules: PASSWORD_RULES,
      copy: {
        forgotPasswordLink: "Forgot password?",
        registrationClosed: "This board doesn’t take open sign-ups. Ask whoever runs it to send you an invite.",
      },
      describeError,
      errorCode,
      providers: toLoginProviders(providers),
      providerSignInUrl: (provider) => apiClient.providerSignInURL(provider, providerReturnPath()),
      passwordLogin,
      redirect: redirectBrowserTo,
    }),
    [signIn, registrationOpen, passwordLogin, providers, navigation],
  );
}
