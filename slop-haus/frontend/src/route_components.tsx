import { useState } from "react";
import { lazyRoute } from "@/shared/lazy_route/lazy_route";
import {
  AuthPagesProvider,
  LoginPage,
  readAndClearCallbackError,
} from "@slop-factory/auth";
import { Outlet, getRouteApi, useAuthNavigation } from "@slop-factory/router";
import { useAuth } from "@/context/auth";
import { useSlopHausAuthPagesConfig } from "@/auth_pages_config";
import { AUTH_ROUTES } from "@/auth_routes";
import AppFooter from "./privacy/app_footer";

// The legal pages are public but rarely the first thing a visitor loads, and
// carry the consent copy: a chunk of their own.
const LegalPage = lazyRoute(() => import("./privacy/legal_page"));

// Route components that adapt an existing page to its route. Kept out of
// routes.tsx so that file exports no components (fast refresh).

export function RootLayout() {
  const navigation = useAuthNavigation(AUTH_ROUTES);
  const authPagesConfig = useSlopHausAuthPagesConfig(navigation);
  // AuthPagesProvider wraps every route, not just the five auth pages --
  // invite/authorize/device render the same AuthLayout/AuthNotice and need
  // the same brand from context.
  return (
    <AuthPagesProvider config={authPagesConfig}>
      <Outlet />
      <AppFooter />
    </AuthPagesProvider>
  );
}

// LoginRoute reads the code a refused provider sign-in came back with
// (/login?error=<code>) once, and scrubs it so a reload doesn't repeat it.
export function LoginRoute() {
  const { notice } = useAuth();
  const [callbackError] = useState(readAndClearCallbackError);
  return <LoginPage notice={notice} callbackError={callbackError} />;
}

const legalRouteApi = getRouteApi("/legal/$kind");
const legalVersionRouteApi = getRouteApi("/legal/$kind/$version");

export function LegalRoute() {
  const { kind } = legalRouteApi.useParams();
  return <LegalPage kind={kind} />;
}

export function LegalVersionRoute() {
  const { kind, version } = legalVersionRouteApi.useParams();
  return <LegalPage kind={kind} version={version} />;
}
