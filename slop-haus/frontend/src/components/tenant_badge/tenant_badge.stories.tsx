import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { TENANT_COLOURS, type TenantColour } from "@/lib/tenant_colour";
import TenantBadge from "./tenant_badge";

// The app ships one dark theme; the palette also carries light values, which
// a story shows by setting the attribute their tokens hang from.
function themeSetup(theme: "dark" | "light") {
  return () => {
    if (theme === "light") document.documentElement.dataset.theme = "light";
    return () => {
      delete document.documentElement.dataset.theme;
    };
  };
}

function Palette() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, padding: 16, background: "var(--color-bg-raised)" }}>
      {TENANT_COLOURS.map((colour: TenantColour) => (
        <TenantBadge key={colour} name={`${colour} workspace`} colour={colour} />
      ))}
    </div>
  );
}

const meta = {
  title: "Widgets/TenantBadge",
  component: Palette,
} satisfies Meta<typeof Palette>;

export default meta;
type Story = StoryObj<typeof meta>;

async function everyColourShows(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  for (const colour of TENANT_COLOURS) {
    await expect(await canvas.findByText(`${colour} workspace`)).toBeVisible();
  }
  await expect(canvasElement.querySelectorAll("[data-colour]")).toHaveLength(TENANT_COLOURS.length);
}

export const Dark: Story = {
  beforeEach: themeSetup("dark"),
  play: async ({ canvasElement }) => everyColourShows(canvasElement),
};

export const Light: Story = {
  beforeEach: themeSetup("light"),
  play: async ({ canvasElement }) => {
    await everyColourShows(canvasElement);
    await expect(document.documentElement.dataset.theme).toBe("light");
  },
};

export const Compact: StoryObj<typeof TenantBadge> = {
  render: () => <TenantBadge name="Ada's workspace" colour="teal" compact />,
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("img", { name: "Ada's workspace" })).toBeVisible();
  },
};
