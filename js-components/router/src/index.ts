// Apps build their routes from these rather than importing
// @tanstack/react-router themselves. That buys two things:
//  - one upgrade point: TanStack's version is pinned only in this package,
//    so an app must not depend on or import it directly;
//  - the same URL conventions and safety in every app, from
//    createAppRouter and the helpers below: flat string search params, no
//    preloading, no trailing slashes, safeRedirectPath for untrusted
//    return-to paths, and guards that replace the history entry rather
//    than push one.
// It does not make the router library swappable. The routing API
// re-exported here is TanStack's own, with its signatures, so each app's
// route tree and route components are written against TanStack; changing
// router library means rewriting those, not just this package.
export {
  Link,
  Outlet,
  RouterProvider,
  createRootRouteWithContext,
  createRoute,
  getRouteApi,
  linkOptions,
  useLocation,
  useMatchRoute,
  useNavigate,
  useParams,
  useRouter,
  useSearch,
  type Register,
  type RouteComponent,
} from "@tanstack/react-router";
export { createAppRouter, type CreateAppRouterOptions, type HistoryOption } from "./router";
export { redirectTo, requireAuthenticated, type RequireAuthenticatedOptions } from "./guards";
export { safeRedirectPath } from "./safe_redirect_path";
export { RETURN_TO_PARAM, loginPathReturningTo, returnToSearch } from "./return_to";
export { noSearch, optionalOneOf, optionalString, parseSearch, stringifySearch, type RawSearch } from "./search";
export { positiveIntParam, uuidParam } from "./params";
export { useAuthNavigation, useNavigateToPath, type AuthNavigation } from "./auth_navigation";
