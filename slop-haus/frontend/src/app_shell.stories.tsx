import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Setting } from "@/api";
import {
  makeAgent,
  makeMember,
  makeProfile,
  makeProposal,
  makeTenant,
} from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { AppShell } from "./app_shell";

// Story-only: the one setting the header reads.
const makeSetting = (name: string): Setting =>
  ({
    name,
    type: "bool",
    bool_val: true,
    int_val: 1,
    string_val: null,
  }) as Setting;

// The panels render into the shell's Outlet, and the test router has no
// routes, so only the shell's own header and the requests it makes show.
let tenantsRead = false;

function stubShellRequests(
  proposals = [makeProposal()],
  tenants = [makeTenant()],
) {
  tenantsRead = false;
  stubApi({
    listAgents: async () => [makeAgent()],
    listMembers: async () => [makeMember()],
    listMemberNames: async () => ({ members: [makeMember()], former: [] }),
    listProposals: async () => proposals,
    listArchivedTenants: async () => [],
    listTenants: async () => {
      tenantsRead = true;
      return tenants;
    },
    getSetting: async (name) => makeSetting(name),
    streamBoardChanges: () => () => {},
  });
}

// A token's computed colour, as getComputedStyle reports it (rgb()), by
// painting a probe with it.
function tokenColor(token: string): string {
  const probe = document.createElement("span");
  probe.style.color = `var(--color-${token})`;
  document.body.append(probe);
  const colour = getComputedStyle(probe).color;
  probe.remove();
  return colour;
}

const meta = {
  title: "Pages/AppShell",
  component: AppShell,
  parameters: { layout: "fullscreen" },
  beforeEach: () => stubShellRequests(),
} satisfies Meta<typeof AppShell>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SignedIn: Story = {
  play: async ({ canvas }) => {
    // The account links are in the menu named for the person.
    await expect(canvas.queryByRole("button", { name: "Sign out" })).toBeNull();
    await userEvent.click(await canvas.findByRole("button", { name: "Ada Lovelace" }));
    await expect(
      await canvas.findByRole("button", { name: "Sign out" }),
    ).toBeVisible();
    await expect(canvas.getByRole("link", { name: "New tenant" })).toBeVisible();
    await expect(
      await canvas.findByRole("status", { name: "1 waiting for approval" }),
    ).toBeVisible();
    await expect(
      await canvas.findByRole("switch", { name: "Ticket processing" }),
    ).toBeChecked();
    // Settled: the tenant list has been read, and one tenant offers no picker.
    await waitFor(() => expect(tenantsRead).toBe(true));
    await expect(canvas.queryByRole("combobox")).toBeNull();
    await expect(canvas.queryByRole("link", { name: "Admin" })).toBeNull();
  },
};

export const AdminWithTenantSwitcher: Story = {
  parameters: { harness: { profile: makeProfile({ is_admin: true }) } },
  beforeEach: () =>
    stubShellRequests(
      [],
      [makeTenant({ current: true }), makeTenant({ id: "t-2", name: "Navy" })],
    ),
  play: async ({ canvas }) => {
    await userEvent.click(await canvas.findByRole("button", { name: "Ada Lovelace" }));
    await expect(
      await canvas.findByRole("link", { name: "Admin" }),
    ).toBeVisible();
    // More than one tenant: the switcher names this one, under a stripe,
    // and stands in for the badge the header has no room to repeat it in.
    const switcher = await canvas.findByRole("combobox", { name: "Tenant" });
    await expect(switcher).toBeVisible();
    await expect(switcher).toHaveTextContent("Ada's workspace");
    await expect(document.querySelector(".tenant-badge")).toBeNull();
    await expect(document.querySelector(".tenant-stripe")).not.toBeNull();
  },
};

// A laptop's width, the narrowest the desktop header is laid out to keep
// one row at (styles/_layout.scss header-names).
const LAPTOP = {
  parameters: {
    viewport: {
      options: { laptop: { name: "Laptop", styles: { width: "1280px", height: "800px" }, type: "desktop" } },
    },
  },
  globals: { viewport: { value: "laptop", isRotated: false } },
} as const;

// Every item in the header shares a line: what the More and account menus,
// and the logo and initials below a wide screen, make room for.
async function expectOneRow(canvasElement: HTMLElement) {
  await expect(window.innerWidth).toBe(1280);
  const header = canvasElement.querySelector<HTMLElement>(".app__header")!;
  // One row is every item's vertical extent overlapping every other's: a
  // wrapped item starts below where the first row's items end.
  const rects = [...header.children]
    .filter((child) => !child.classList.contains("tenant-stripe"))
    .map((child) => child.getBoundingClientRect());
  await expect(Math.max(...rects.map((r) => r.top))).toBeLessThan(Math.min(...rects.map((r) => r.bottom)));
}

export const DesktopOneRow: Story = {
  ...LAPTOP,
  play: async ({ canvas, canvasElement }) => {
    await canvas.findByRole("button", { name: "Ada Lovelace" });
    await canvas.findByRole("status", { name: "1 waiting for approval" });
    await expectOneRow(canvasElement);

    await userEvent.click(canvas.getByRole("button", { name: "More" }));
    await expect(canvas.getByRole("group", { name: "Settings" })).toBeVisible();
    await expect(canvas.getByRole("link", { name: "Tenant settings" })).toBeVisible();
  },
};

// The widest the row gets at a laptop's width: a tenant switcher and a
// two-digit approvals count.
export const DesktopOneRowSeveralTenants: Story = {
  ...LAPTOP,
  beforeEach: () =>
    stubShellRequests(
      Array.from({ length: 12 }, (_, i) => makeProposal({ id: `p-${i}` })),
      [makeTenant({ current: true }), makeTenant({ id: "t-2", name: "Navy" })],
    ),
  play: async ({ canvas, canvasElement }) => {
    await canvas.findByRole("combobox", { name: "Tenant" });
    await canvas.findByRole("status", { name: "12 waiting for approval" });
    await expectOneRow(canvasElement);

    // The account menu drops from its button's end edge, so it stays on
    // screen at the header's right.
    await userEvent.click(canvas.getByRole("button", { name: "Ada Lovelace" }));
    const signOut = await canvas.findByRole("button", { name: "Sign out" });
    const panel = signOut.closest<HTMLElement>(".header-menu__panel")!;
    await expect(panel.getBoundingClientRect().right).toBeLessThanOrEqual(window.innerWidth);
  },
};

export const PhoneWidth: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvas }) => {
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    const menu = await canvas.findByRole("button", { name: "Menu" });
    await userEvent.click(menu);
    await expect(
      await canvas.findByRole("button", { name: "Sign out" }),
    ).toBeVisible();

    // The group names are section labels in the accent colour, and the
    // links under them are full-strength text, indented behind a rule.
    const group = canvas.getByRole("group", { name: "Tickets" });
    const label = group.querySelector<HTMLElement>(".app__nav-group-label")!;
    const [current, link] = within(group).getAllByRole("link");
    await expect(getComputedStyle(current).color).toBe(
      tokenColor("accent-hot"),
    );
    await expect(getComputedStyle(label).color).toBe(tokenColor("accent"));
    await expect(getComputedStyle(link).color).toBe(tokenColor("text"));
    await expect(getComputedStyle(link).color).not.toBe(
      tokenColor("text-muted"),
    );
    await expect(label).toBeVisible();
    await expect(label).toHaveTextContent("Tickets");
    for (const item of [current, link]) {
      await expect(
        parseFloat(getComputedStyle(item).borderLeftWidth),
      ).toBeGreaterThan(0);
      await expect(item.getBoundingClientRect().left).toBeGreaterThan(
        label.getBoundingClientRect().left,
      );
    }
  },
};
