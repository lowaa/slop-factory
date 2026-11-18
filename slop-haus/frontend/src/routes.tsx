import {
  RegisterPage,
  ActivatePage,
  ForgotPasswordPage,
  ResetPasswordPage,
} from "@slop-factory/auth";
import {
  createAppRouter,
  createRootRouteWithContext,
  createRoute,
  noSearch,
  optionalOneOf,
  optionalString,
  positiveIntParam,
  redirectTo,
  requireAuthenticated,
  returnToSearch,
  type HistoryOption,
  type RouteComponent,
} from "@slop-factory/router";
import type { AuthStatus } from "@/context/auth";
import { AUTH_ROUTES, signInReturnPath } from "@/auth_routes";
import { RouteErrorFallback } from "@/error_reporting/error_boundary";
import { AppShell } from "./app_shell";
import { RootLayout, LoginRoute, LegalRoute, LegalVersionRoute } from "./route_components";
import { LEGACY_AGENTS_PATH, tabNameFromSearch, type Tab } from "./tabs";
import { BOARD_STATUSES } from "@/domain/board_columns";
import { parseBoardSearch, type BoardSearch } from "@/domain/board_filter";
import { BoardRoute } from "@/components/board_view";
import { parseBlockedSearch, BlockedRoute } from "@/components/blocking_graph";
import { parseUnreviewedSearch } from "@/domain/unreviewed_filter";
import { UnreviewedRoute } from "@/components/unreviewed_panel";
import { parseApprovalsSearch } from "@/domain/approvals_filter";
import { ApprovalsRoute } from "@/components/approvals_panel";
import { StewardDigestPage, StewardNotices } from "@/components/steward_digest";
import { ComparePage } from "@/components/compare_page";
import { REPORT_DAYS, REPORT_PRIORITIES, parseUsageTicketsSearch, ReportsRoute } from "@/components/reports_panel";
import { parseRemoteControlView, RemoteControlRoute, type RemoteControlView } from "@/components/remote_control";
import { EvalSetsPage } from "@/components/eval_sets";
import { SupervisorsPage } from "@/components/supervisors_page";
import { HelpIndexRoute, HelpPageRoute } from "@/components/help";
import { AgentsPanel } from "@/components/agents_panel";
import { WorkspacePanel } from "@/components/workspace_panel";
import { MembersPanel } from "@/components/members_panel";
import { TenantSettingsPage } from "@/components/tenant_settings_page";
import { GitTab } from "@/components/git_tab";
import { IntakeSourcesPanel } from "@/components/intake_sources_panel";
import { NewTenantPage, NEW_TENANT_PATH } from "@/components/new_tenant_page";
import { AccountRoute, ACCOUNT_PATH } from "@/components/account_page";
import { InviteRoute, hasPendingInviteToken } from "@/components/invite_page";
import { TicketRoute, TicketAttemptRoute, TICKET_VIEWS } from "@/components/ticket_detail";
import { GitHubAppCreatedRoute, GitHubAppInstalledRoute } from "@/components/github_app_callback";
import {
  AuthorizePage,
  captureAuthorizeRequestId,
  hasPendingAuthorizeRequest,
} from "@/components/authorize_page";
import { DevicePage, captureDeviceParams, hasPendingDeviceVisit } from "@/components/device_page";

// The router only ever runs once AuthProvider knows whether anyone is
// signed in (app.tsx shows "Loading…" until then), so its guards never see
// "loading".
export interface RouterContext {
  authStatus: Exclude<AuthStatus, "loading">;
  // Whether a visitor sent to /login takes the page they were after with
  // them (?redirect=), to land back on it once signed in. Not after a
  // deliberate sign-out: see AuthContextValue.signedOutByUser.
  rememberReturnPath: boolean;
}

// The pages a signed-in user is sent back to after a detour through
// /login, in the order they take precedence. The URL by then says only
// "/login", so the pending_* modules' own flags are what remember there is
// an invite, a consent request or a device code still to finish. They are
// module state, which is why every move between these pages and /login has
// to stay an in-app navigation: a full page load would forget them.
const PENDING_FLOWS: { path: string; isPending: () => boolean }[] = [
  { path: "/invite", isPending: hasPendingInviteToken },
  { path: "/authorize", isPending: hasPendingAuthorizeRequest },
  { path: "/device", isPending: hasPendingDeviceVisit },
];

function pendingFlowPath(): string | null {
  return PENDING_FLOWS.find((flow) => flow.isPending())?.path ?? null;
}

// earlierPendingFlowPath is a pending flow that takes precedence over the
// one at path: a signed-in visitor on /device with an invite still pending
// finishes the invite first. The page at path itself is shown whether or
// not it has anything pending -- /authorize with no request id explains
// that it is missing one.
function earlierPendingFlowPath(path: string): string | null {
  const earlier = PENDING_FLOWS.slice(
    0,
    PENDING_FLOWS.findIndex((flow) => flow.path === path),
  );
  return earlier.find((flow) => flow.isPending())?.path ?? null;
}

// Where a signed-in user landing on a page meant for signed-out ones (a
// bookmarked /login, say, or /login just signed in on) goes instead: a
// flow left pending on this page first -- it is what they were in the
// middle of -- then the page /login was asked to return to.
function signedInLanding(returnTo?: string): string {
  return pendingFlowPath() ?? signInReturnPath(returnTo);
}

// The guard for /authorize and /device: signed out, to /login (the page
// has already stashed what it needs for the way back); signed in, to an
// earlier pending flow if there is one.
function signedInFlow(
  path: string,
  authStatus: RouterContext["authStatus"],
): void {
  if (authStatus !== "authenticated") redirectTo(AUTH_ROUTES.login);
  const earlier = earlierPendingFlowPath(path);
  if (earlier) redirectTo(earlier);
}

// A refused provider sign-in, link or step-up comes back to the page it
// started from with ?error=<code>; the page reads it (and scrubs it) with
// readAndClearCallbackError.
function callbackErrorSearch(raw: Record<string, unknown>): { error?: string } {
  return { error: optionalString(raw, "error") };
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
});

// The sign-in pages: signed in, there is nothing to do on them, so a
// signed-in visitor is sent on (to a pending flow if there is one).
function signedOutOnly({ context }: { context: RouterContext }): void {
  if (context.authStatus === "authenticated") redirectTo(signedInLanding());
}

// /login?redirect=<path> is where a signed-in route sent an anonymous
// visitor from, to go back to once they sign in.
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: AUTH_ROUTES.login,
  validateSearch: (raw: Record<string, unknown>) => ({
    ...callbackErrorSearch(raw),
    ...returnToSearch(raw),
  }),
  beforeLoad: ({ context, search }) => {
    if (context.authStatus === "authenticated")
      redirectTo(signedInLanding(search.redirect));
  },
  component: LoginRoute,
});

const registerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: AUTH_ROUTES.register,
  validateSearch: noSearch,
  beforeLoad: signedOutOnly,
  component: RegisterPage,
});

const forgotPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: AUTH_ROUTES.forgotPassword,
  validateSearch: noSearch,
  beforeLoad: signedOutOnly,
  component: ForgotPasswordPage,
});

// /activate and /reset-password stay reachable even signed in: an
// activation link opened in a browser that's already signed in to a
// different account, or a password reset started from elsewhere, are both
// real situations and neither should silently redirect to the board
// mid-token. Their token is in the fragment, not the search.
//
// "/reset-password" rather than "/password-reset/confirm" or similar:
// nothing here has to match an existing emailed link's path, so the
// plainest name won.
const activateRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/activate",
  validateSearch: noSearch,
  component: ActivatePage,
});

const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/reset-password",
  validateSearch: noSearch,
  component: ResetPasswordPage,
});

// /invite likewise, signed in or not: an invite for an address the visitor
// is already signed in as (or a different one) both have real things to
// show rather than a silent redirect to the board. Its token is in the
// fragment.
const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/invite",
  validateSearch: callbackErrorSearch,
  component: InviteRoute,
});

// The legal documents are public, signed in or out: a visitor reads the
// policy before choosing anything, and the footer on every page links here.
const legalRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/legal/$kind",
  validateSearch: noSearch,
  component: LegalRoute,
});

const legalVersionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/legal/$kind/$version",
  validateSearch: noSearch,
  component: LegalVersionRoute,
});

// /authorize and /device both need a signed-in user to decide as, and both
// stash whatever brought a visitor here (a request id; a device code and
// agent name) before an anonymous one is sent to /login -- the URL that
// lands back on /login no longer carries any of it (see
// pending_authorize.ts / pending_device.ts).
const authorizeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/authorize",
  validateSearch: (raw: Record<string, unknown>) => ({
    request: optionalString(raw, "request"),
  }),
  beforeLoad: ({ context }) => {
    captureAuthorizeRequestId();
    signedInFlow("/authorize", context.authStatus);
  },
  component: AuthorizePage,
});

const deviceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/device",
  validateSearch: (raw: Record<string, unknown>) => ({
    code: optionalString(raw, "code"),
    agent: optionalString(raw, "agent"),
  }),
  beforeLoad: ({ context }) => {
    captureDeviceParams();
    signedInFlow("/device", context.authStatus);
  },
  component: DevicePage,
});

// The signed-in app: every route under this layout needs a session -- an
// anonymous visitor goes to /login and comes back here afterwards -- and a
// pending flow from a /login detour takes precedence over it.
const signedInRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "signed-in",
  beforeLoad: ({ context, location }) => {
    requireAuthenticated({
      authenticated: context.authStatus === "authenticated",
      loginPath: AUTH_ROUTES.login,
      returnTo: context.rememberReturnPath ? location.href : undefined,
    });
    const pending = pendingFlowPath();
    if (pending) redirectTo(pending);
  },
  component: AppShell,
});

// Each header tab (tabs.ts) is a route, so Back/Forward and a reload keep
// the tab. Path is typed to tabs.ts's paths, so the two can't drift.
function tabRoute<TPath extends Tab["path"]>(
  path: TPath,
  component: RouteComponent,
) {
  return createRoute({
    getParentRoute: () => signedInRoute,
    path,
    validateSearch: noSearch,
    component,
  });
}

// The board is the one tab with a search of its own: ?column= is the
// column a phone shows (see board.tsx), and ?done=, ?parked= and ?q= its filters
// (board_view/board_filter.ts), so a filtered board is a link that can be shared.
// The type is board_filter.ts's BoardSearch.

const boardRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/" satisfies Tab["path"],
  // Typed optional, not just possibly undefined: a link to the board
  // should not have to name a column or a filter.
  validateSearch: (raw: Record<string, unknown>): BoardSearch => ({
    ...parseBoardSearch(raw),
    column: optionalOneOf(raw, "column", BOARD_STATUSES),
  }),
  component: BoardRoute,
});
// ?conductor= and ?session= are the machine and the session on show. Without a
// session the page opens the connected one if there is one, or else a new
// session on that machine; ?new=1 asks for a new session even when one is
// connected. ?draft= is what a message box starts with, the connected
// session's or a new one's: a ticket's "discuss" shortcut links here with it.
const remoteControlRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/remote-control" satisfies Tab["path"],
  // Typed optional, so a link names only what it means to.
  validateSearch: (
    raw: Record<string, unknown>,
  ): { conductor?: string; session?: string; draft?: string; new?: 1; view?: RemoteControlView } => ({
    conductor: optionalString(raw, "conductor"),
    session: optionalString(raw, "session"),
    draft: optionalString(raw, "draft"),
    view: parseRemoteControlView(raw.view),
    // The router parses "1" to a number; true is what a hand-written link may say.
    new: raw.new === 1 || raw.new === "1" || raw.new === true ? 1 : undefined,
  }),
  component: RemoteControlRoute,
});
const blockedRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/blocked" satisfies Tab["path"],
  validateSearch: parseBlockedSearch,
  component: BlockedRoute,
});
// The steward's notices sit above the Approvals page: what it will apply and
// has applied, with veto and undo.
function ApprovalsWithStewardNotices() {
  return (
    <>
      <StewardNotices />
      <ApprovalsRoute />
    </>
  );
}
const approvalsRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/approvals" satisfies Tab["path"],
  validateSearch: parseApprovalsSearch,
  component: ApprovalsWithStewardNotices,
});
const stewardRoute = tabRoute("/steward", StewardDigestPage);
const unreviewedRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/unreviewed" satisfies Tab["path"],
  validateSearch: parseUnreviewedSearch,
  component: UnreviewedRoute,
});
// ?days= and ?priority= are the Reports tab's filters, left out at their
// defaults (30 days, every priority) so the plain tab is "/reports". The
// by-ticket usage list's filter is in the address too (?q=, ?exclude=,
// ?created_from=, ?created_to=; see usage_tickets_filter.ts).
const reportsRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/reports" satisfies Tab["path"],
  validateSearch: (raw: Record<string, unknown>) => ({
    days: optionalOneOf(raw, "days", REPORT_DAYS),
    priority: optionalOneOf(raw, "priority", REPORT_PRIORITIES),
    include_parked: optionalOneOf(raw, "include_parked", ["1"] as const),
    ...parseUsageTicketsSearch(raw),
  }),
  component: ReportsRoute,
});
const compareRoute = tabRoute("/compare", ComparePage);
const workspaceRoute = tabRoute("/workspace", WorkspacePanel);
// GitHub's redirects back from creating and installing the App (see
// github_app_callback.tsx); their search is whatever GitHub appends.
function githubAppCallbackSearch(raw: Record<string, unknown>) {
  return {
    code: optionalString(raw, "code"),
    state: optionalString(raw, "state"),
    installation_id: optionalString(raw, "installation_id"),
    setup_action: optionalString(raw, "setup_action"),
  };
}
const githubAppCreatedRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/workspace/github-app/created",
  validateSearch: githubAppCallbackSearch,
  component: GitHubAppCreatedRoute,
});
const githubAppInstalledRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/workspace/github-app/installed",
  validateSearch: githubAppCallbackSearch,
  component: GitHubAppInstalledRoute,
});
const workersRoute = tabRoute("/workers" satisfies Tab["path"], AgentsPanel);
const addMachineRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/workers/add-machine",
  validateSearch: noSearch,
  component: SupervisorsPage,
});
const legacyAgentsRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: LEGACY_AGENTS_PATH,
  validateSearch: noSearch,
  beforeLoad: () => redirectTo("/workers"),
});
const membersRoute = tabRoute("/members", MembersPanel);
const settingsRoute = tabRoute("/settings", TenantSettingsPage);
const gitRoute = tabRoute("/git", GitTab);
const intakeRoute = tabRoute("/intake", IntakeSourcesPanel);
const evalSetsRoute = tabRoute("/eval-sets", EvalSetsPage);

// A ticket's number is only unique within its tenant, so a link to one
// carries the tenant it is in (?tenant=) and TicketRoute will not show it
// from a different one. ?from= is the tab it was opened from, and ?view=
// the ticket's own tab (ticket_view.ts), left out for its main one.
const validateTicketSearch = (raw: Record<string, unknown>) => ({
  from: tabNameFromSearch(raw.from),
  tenant: optionalString(raw, "tenant"),
  view: optionalOneOf(raw, "view", TICKET_VIEWS),
});

const ticketRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/tickets/$ticketId",
  params: positiveIntParam("ticketId"),
  validateSearch: validateTicketSearch,
  component: TicketRoute,
});

// One attempt of a ticket (/tickets/42/attempts/2): the same page, on that
// attempt's tab. Both numbers must be positive integers or the route does
// not match.
const ticketIdParam = positiveIntParam<"ticketId", "n">("ticketId");
const attemptNumberParam = positiveIntParam<"n", "ticketId">("n");
const ticketAttemptRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/tickets/$ticketId/attempts/$n",
  params: {
    parse: (raw: { ticketId: string; n: string }) => {
      const id = ticketIdParam.parse(raw);
      const n = attemptNumberParam.parse(raw);
      return id && n ? { ticketId: id.ticketId, n: n.n } : false;
    },
    stringify: (params: { ticketId: number; n: number }) => ({
      ticketId: String(params.ticketId),
      n: String(params.n),
    }),
  },
  validateSearch: validateTicketSearch,
  component: TicketAttemptRoute,
});

// /account is not a tab. A link or step-up through a login provider comes
// back to it with ?error= when refused.
const accountRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: ACCOUNT_PATH,
  validateSearch: callbackErrorSearch,
  component: AccountRoute,
});

const newTenantRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: NEW_TENANT_PATH,
  validateSearch: noSearch,
  component: NewTenantPage,
});

// The user guide: bundled pages (docs/guide), open to every signed-in member.
const helpRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/help",
  validateSearch: noSearch,
  component: HelpIndexRoute,
});

const helpPageRoute = createRoute({
  getParentRoute: () => signedInRoute,
  path: "/help/$slug",
  validateSearch: noSearch,
  component: HelpPageRoute,
});

// Any other path: the board if signed in (or a pending flow), else /login.
const unknownRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "$",
  validateSearch: noSearch,
  beforeLoad: ({ context }) => {
    redirectTo(
      context.authStatus === "authenticated"
        ? signedInLanding()
        : AUTH_ROUTES.login,
    );
  },
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  registerRoute,
  forgotPasswordRoute,
  activateRoute,
  resetPasswordRoute,
  inviteRoute,
  legalRoute,
  legalVersionRoute,
  authorizeRoute,
  deviceRoute,
  signedInRoute.addChildren([
    boardRoute,
    remoteControlRoute,
    blockedRoute,
    approvalsRoute,
    stewardRoute,
    unreviewedRoute,
    reportsRoute,
    compareRoute,
    workspaceRoute,
    githubAppCreatedRoute,
    githubAppInstalledRoute,
    workersRoute,
    addMachineRoute,
    legacyAgentsRoute,
    membersRoute,
    settingsRoute,
    gitRoute,
    intakeRoute,
    evalSetsRoute,
    ticketRoute,
    ticketAttemptRoute,
    accountRoute,
    newTenantRoute,
    helpRoute,
    helpPageRoute,
  ]),
  unknownRoute,
]);

export function createSlopHausRouter(history?: HistoryOption) {
  return createAppRouter({
    routeTree,
    // Replaced on every render by app.tsx's RouterProvider, once the
    // session is known; the router never loads before then.
    context: {
      authStatus: "anonymous",
      rememberReturnPath: true,
    } satisfies RouterContext,
    history,
    errorComponent: RouteErrorFallback,
  });
}

export type SlopHausRouter = ReturnType<typeof createSlopHausRouter>;

// Registers this app's routes with the router's types, so a Link's `to`
// and a navigate() call are checked against the paths declared above.
declare module "@slop-factory/router" {
  interface Register {
    router: SlopHausRouter;
  }
}
