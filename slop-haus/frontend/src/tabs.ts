// The header's tabs, each a route of its own, in the groups the header
// clusters them by. A ticket is not a tab: it rolls up under whichever tab it
// was opened from (its ?from=), which is both where its Back link goes and
// which tab reads as active meanwhile.
//
// "agent" is the umbrella term (a worker or a conductor), so the Workers tab
// names only the former; Remote Control is where the latter is driven.
//
// Members is its own tab rather than folded into Workers: workers are the
// non-human fleet this board coordinates, invites and membership are about
// who has an account on it at all -- different question, different audience.
//
// Unreviewed is the done tickets whose change merged without a person
// looking at it first, for looking at afterwards. Its own tab rather than a
// board column: it is a to-do list worked through at leisure, not a stage
// of live work, and Done on the board already shows every finished ticket.
//
// Workspace sits directly right of Workers: it shows how each worker's
// workspace looks on disk and, in time, the manifest that says how it should.
//
// Integrations is the external systems this board is wired to, between the
// agents that do the work and the settings about the board itself. Git is the
// GitHub App and webhooks (the code host); Intake is the sources that file
// tickets from outside. Each is its own tab because each is configured on its
// own and has its own deliveries to inspect; neither belongs on Workspace
// (what a worker's checkout looks like) or Members (who has an account).
//
// Reports is where the time goes: lead time, time in each state and
// throughput over a period. Its own tab because it answers "how are we
// doing", which no board column or list shows.
//
// Compare answers whether a variant is good enough against a baseline across
// many tickets, beside Reports because both are about how the work is going
// rather than the work itself.
//
// Tenant settings is the tenant-wide switches (with who changed them) and
// the signed-in person's own settings in this tenant, such as auto-approve.
// Members and Reports sit beside it because all three are about the tenant
// itself rather than the work in it.
//
// Eval sets are named groups of done tickets (a saved filter, or a pinned
// list) that variants are replayed across, with the batches run so far.
//
// Approvals is what the person's agents (conductors, Remote Controls included)
// have proposed for them to approve. An agent can only ask: nothing it
// proposes happens until it is approved here. Steward is the digest of what
// the steward agent approved on the owner's behalf, with its agreement rate.
//
// inMore puts a group's tabs behind the desktop header's More menu, which is
// what keeps the header to one row: Integrations and Settings are set up once
// and visited now and then, where the Tickets and Agents tabs are the day's
// work. The phone menu lists every group alike.
export const TAB_GROUPS = [
  {
    name: "tickets",
    inMore: false,
    label: "Tickets",
    tabs: [
      { name: "board", label: "Board", path: "/", backLabel: "← Back to board" },
      { name: "approvals", label: "Approvals", path: "/approvals", backLabel: "← Back to approvals" },
      { name: "steward", label: "Steward", path: "/steward", backLabel: "← Back to steward" },
      { name: "blocked", label: "Blocked", path: "/blocked", backLabel: "← Back to blocked" },
      { name: "unreviewed", label: "Unreviewed", path: "/unreviewed", backLabel: "← Back to unreviewed" },
    ],
  },
  {
    name: "agents",
    inMore: false,
    label: "Agents",
    tabs: [
      { name: "remote_control", label: "Remote Control", path: "/remote-control", backLabel: "← Back to Remote Control" },
      { name: "workers", label: "Workers", path: "/workers", backLabel: "← Back to workers" },
      { name: "workspace", label: "Workspace", path: "/workspace", backLabel: "← Back to workspace" },
    ],
  },
  {
    name: "integrations",
    inMore: true,
    label: "Integrations",
    tabs: [
      { name: "git", label: "Git", path: "/git", backLabel: "← Back to Git" },
      { name: "intake", label: "Intake", path: "/intake", backLabel: "← Back to intake" },
    ],
  },
  {
    name: "settings",
    inMore: true,
    label: "Settings",
    tabs: [
      { name: "reports", label: "Reports", path: "/reports", backLabel: "← Back to reports" },
      { name: "compare", label: "Compare", path: "/compare", backLabel: "← Back to compare" },
      { name: "eval_sets", label: "Eval sets", path: "/eval-sets", backLabel: "← Back to eval sets" },
      { name: "members", label: "Members", path: "/members", backLabel: "← Back to members" },
      { name: "tenant_settings", label: "Tenant settings", path: "/settings", backLabel: "← Back to settings" },
    ],
  },
] as const;

export type Tab = (typeof TAB_GROUPS)[number]["tabs"][number];
export type TabGroup = (typeof TAB_GROUPS)[number];

export const BAR_GROUPS: readonly TabGroup[] = TAB_GROUPS.filter((group) => !group.inMore);
export const MORE_GROUPS: readonly TabGroup[] = TAB_GROUPS.filter((group) => group.inMore);

export const TABS: readonly Tab[] = TAB_GROUPS.flatMap((group) => [...group.tabs] as Tab[]);
export type TabName = Tab["name"];

const TAB_NAMES: readonly TabName[] = TABS.map((tab) => tab.name);

// /agents was the Workers tab before "agent" became the umbrella term for
// workers and conductors. Old bookmarks and ?from=agents ticket links still
// land on it.
export const LEGACY_AGENTS_PATH = "/agents";
export function tabNameFromSearch(name: unknown): TabName | undefined {
  if (name === "agents") return "workers";
  return TAB_NAMES.find((tab) => tab === name);
}

// A ticket link without ?from= (typed by hand, or from before tabs had
// URLs) goes back to the board, as a ticket opened from it would.
export function tabNamed(name: TabName | undefined): Tab {
  return TABS.find((tab) => tab.name === name) ?? TABS[0];
}
