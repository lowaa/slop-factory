import { Link, Outlet, useMatchRoute, useSearch } from "@slop-factory/router";
import { useAuth } from "@/context/auth";
import { SessionInterruptProvider, useSessionInterrupt } from "@/context/session_interrupt";
import { TenantArchived } from "@/components/tenant_archived";
import { TenantChanged } from "@/components/tenant_changed";
import { AgentNamesProvider } from "@/shared/names/agent_name";
import { MemberNamesProvider } from "@/shared/names/member_name";
import { ProcessingToggle } from "@/components/processing_toggle";
import { TenantIdentity } from "@/components/tenant_identity";
import { TenantSwitcher } from "@/components/tenant_switcher";
import { ACCOUNT_PATH } from "@/components/account_page";
import { NEW_TENANT_PATH } from "@/components/new_tenant_page";
import { HeaderMenu } from "@/components/header_menu";
import { ProposalsProvider } from "@/components/proposals";
import { useProposals } from "@/components/proposals";
import { useNarrowLayout } from "./lib/layout";
import type { Profile } from "@/api";
import { BAR_GROUPS, MORE_GROUPS, TABS, TAB_GROUPS, tabNamed, type Tab, type TabGroup, type TabName } from "./tabs";
import { BrandLogo } from "@/components/brand_logo";
import { tenantInitials } from "@/lib/tenant_colour";
import "./app.scss";

// useActiveTab is the tab that reads as active: the one on screen, or for
// an open ticket the tab it was opened from. None on /account.
function useActiveTab(): TabName | null {
  const matchRoute = useMatchRoute();
  const search = useSearch({ strict: false });
  if (matchRoute({ to: "/tickets/$ticketId" })) return tabNamed(search.from).name;
  return TABS.find((tab) => matchRoute({ to: tab.path }))?.name ?? null;
}

// PendingBadge is how many proposals wait for the person, on the Approvals
// tab and, on a phone where the tabs are in the menu, on the menu button.
function PendingBadge() {
  const { proposals } = useProposals();
  if (proposals.length === 0) return null;
  return (
    <span className="app__badge" role="status" aria-label={`${proposals.length} waiting for approval`}>
      {proposals.length}
    </span>
  );
}

// The groups are role=group clusters, each named for assistive tech; the
// visible group label shows only where the clusters stack (the phone menu and
// the More panel).
function NavGroups({ groups, activeTab }: { groups: readonly TabGroup[]; activeTab: TabName | null }) {
  return groups.map((group) => (
    <div key={group.name} role="group" aria-label={group.label} className="app__nav-group">
      <span className="app__nav-group-label" aria-hidden="true">
        {group.label}
      </span>
      {group.tabs.map((tab) => (
        <Link
          key={tab.name}
          to={tab.path}
          className={`app__nav-item${tab.name === activeTab ? " app__nav-item--active" : ""}`}
          aria-current={tab.name === activeTab ? "page" : undefined}
        >
          {tab.label}
          {tab.name === "approvals" && <PendingBadge />}
        </Link>
      ))}
    </div>
  ));
}

// MoreMenu holds the groups that do not fit the desktop row. Its button reads
// as the open tab when that tab is one of them, so the header still says
// where the person is with the menu closed.
function MoreMenu({ activeTab }: { activeTab: TabName | null }) {
  const active = MORE_GROUPS.flatMap((group) => [...group.tabs] as Tab[]).find((tab) => tab.name === activeTab);
  return (
    <HeaderMenu trigger={active?.label ?? "More"} active={active !== undefined}>
      <div className="app__more">
        <NavGroups groups={MORE_GROUPS} activeTab={activeTab} />
      </div>
    </HeaderMenu>
  );
}

// The account menu's button: the person's name on a wide screen, their
// initials below it (app.scss), as the wordmark gives way to the logo. The
// name stays the accessible name at every width.
function AccountTrigger({ name }: { name: string }) {
  return (
    <>
      <span className="app__account-initials" aria-hidden="true">
        {tenantInitials(name)}
      </span>
      <span className="app__account-trigger-name">{name}</span>
    </>
  );
}

// The links out of the tabs, about the person rather than the tenant's work.
function AccountLinks({ profile, onSignOut }: { profile: Profile; onSignOut: () => Promise<void> }) {
  return (
    <>
      <Link
        to="/help"
        className="app__account-link"
        activeProps={{ className: "app__account-link--active", "aria-current": "page" }}
      >
        Help
      </Link>
      <Link
        to={ACCOUNT_PATH}
        className="app__account-link"
        activeProps={{ className: "app__account-link--active", "aria-current": "page" }}
      >
        Account
      </Link>
      {/* Here rather than in the tenant switcher: that is a listbox of
          tenants, and only shows for two or more, while a single-tenant
          person is who most needs to find this. */}
      <Link
        to={NEW_TENANT_PATH}
        className="app__account-link"
        activeProps={{ className: "app__account-link--active", "aria-current": "page" }}
      >
        New tenant
      </Link>
      {/* A real anchor, not a router Link: the admin pages are a
          separate Vite build served under /admin/ (see
          vite.admin.config.ts and nginx.conf.template's
          auth_request gate), never part of this SPA's own bundle
          or routing. */}
      {profile.is_admin && (
        <a className="app__admin-link" href="/admin/">
          Admin
        </a>
      )}
      <button type="button" className="app__sign-out" onClick={() => void onSignOut()}>
        Sign out
      </button>
    </>
  );
}

// AppShell is the layout of every signed-in route: the header, and the
// providers that need a session (GET /api/agents and /api/members do), so
// they only mount once there is a signed-in tenant to show.
export function AppShell() {
  return (
    <SessionInterruptProvider>
      <AppShellBody />
    </SessionInterruptProvider>
  );
}

// The interrupt is held above every provider, so the notice replaces the
// whole tab and nothing under it can act again.
function AppShellBody() {
  const interrupt = useSessionInterrupt();
  const { profile, signOut } = useAuth();
  const activeTab = useActiveTab();
  const narrow = useNarrowLayout();
  // The route guard only lets a signed-in session in, but a session that
  // ends while this is on screen re-renders it once before the guard
  // re-runs and redirects to /login.
  if (!profile) return null;
  if (interrupt?.kind === "archived") return <TenantArchived />;
  if (interrupt?.kind === "changed") return <TenantChanged was={interrupt.was} />;

  return (
    <AgentNamesProvider>
      <MemberNamesProvider currentUserId={profile.id}>
        <ProposalsProvider>
          <div className="app">
            <header className={`app__header${narrow ? " app__header--narrow" : ""}`}>
              <h1>
                <BrandLogo size={32} />
                <span className="app__wordmark">slop-haus</span>
              </h1>
              {narrow ? (
                // One row on a phone: the whole desktop header wraps to three
                // or four there. The processing switch stays out of the menu
                // for the reason it is in the header at all.
                <>
                  <TenantIdentity compact />
                  <ProcessingToggle />
                  <HeaderMenu badge={<PendingBadge />}>
                    <nav className="app__nav">
                      <NavGroups groups={TAB_GROUPS} activeTab={activeTab} />
                    </nav>
                    <TenantSwitcher />
                    <div className="app__account">
                      <span className="app__account-name">{profile.display_name}</span>
                      <AccountLinks profile={profile} onSignOut={signOut} />
                    </div>
                  </HeaderMenu>
                </>
              ) : (
                <>
                  <nav className="app__nav">
                    <NavGroups groups={BAR_GROUPS} activeTab={activeTab} />
                    <MoreMenu activeTab={activeTab} />
                  </nav>

                  <TenantIdentity badge={false} />

                  {/* In the header rather than on a settings screen: it is about
                      every tab rather than the one being looked at, and a fleet
                      that has been paused should be visible without going to find
                      out. */}
                  <ProcessingToggle />

                  {/* Only rendered once there's a second tenant to offer -- see
                      tenant_switcher.tsx -- so a single-tenant account (still the
                      common case) sees no picker at all. */}
                  <TenantSwitcher labelHidden />

                  <HeaderMenu trigger={<AccountTrigger name={profile.display_name} />} align="end">
                    <div className="app__account-menu">
                      <AccountLinks profile={profile} onSignOut={signOut} />
                    </div>
                  </HeaderMenu>
                </>
              )}
            </header>

            <main className="app__main">
              <Outlet />
            </main>
          </div>
        </ProposalsProvider>
      </MemberNamesProvider>
    </AgentNamesProvider>
  );
}
