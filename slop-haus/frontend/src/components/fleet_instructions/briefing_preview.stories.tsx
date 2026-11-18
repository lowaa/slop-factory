import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { BriefingFile, FleetInstructions } from "@/api";
import { stubApi } from "@/test/story_harness";
import BriefingPreview from "./briefing_preview";

const V1 = { id: "id-1", name: "house", version: 1, kind: "fleet" as const, body: "Be brief.", created_by: null, created_at: "2026-10-04T12:00:00Z", archived: false, archived_at: null };
const V2 = { ...V1, id: "id-2", version: 2, body: "Comment a plan first." };

const DATA: FleetInstructions = { versions: [V2, V1], default_id: "id-2", can_edit: false, max_body_bytes: 1000 };

const meta = {
  title: "Fleet instructions/BriefingPreview",
  component: BriefingPreview,
  args: { data: DATA },
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 720 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof BriefingPreview>;

export default meta;
type Story = StoryObj<typeof meta>;

function filesFor(versionId: string | null): BriefingFile[] {
  const version = [V1, V2].find((v) => v.id === versionId);
  const fleet = version ? `\n## Fleet instructions\n\nThe protocol takes precedence.\n\n> ${version.body}\n>\n> Then stop.\n` : "";
  return [
    { name: "headless", path: ".slop-haus/headless-briefing.md", protocol: "# slop-haus agent: <agent-name>\n", fleet },
    { name: "pi", path: ".pi/slop-haus.md", protocol: "pi briefing\n", fleet: "" },
  ];
}

// Stateful in the sense the server is: the files depend on the version asked
// for, and every request is recorded.
const requested: (string | null)[] = [];
function stubBriefings(load: (versionId: string | null) => Promise<BriefingFile[]> = async (v) => filesFor(v)) {
  requested.length = 0;
  stubApi({
    getFleetBriefings: async (versionId) => {
      requested.push(versionId);
      return load(versionId);
    },
  });
}

const TOGGLE = /briefing agents receive/;

export const Collapsed: Story = {
  beforeEach: () => stubBriefings(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: TOGGLE })).toHaveAttribute("aria-expanded", "false");
    await expect(canvas.queryByRole("tablist")).toBeNull();
    await expect(requested).toEqual([]);
  },
};

export const OpenOnDefaultVersion: Story = {
  beforeEach: () => stubBriefings(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    await expect(await canvas.findByRole("tablist")).toBeVisible();
    await expect(canvas.getByRole("combobox")).toHaveTextContent("house v2 (default)");
    await expect(requested).toEqual(["id-2"]);
    await expect(canvas.getByRole("tabpanel")).toHaveTextContent("slop-haus agent: <agent-name>");
  },
};

export const SelectingVersions: Story = {
  beforeEach: () => stubBriefings(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    const select = await canvas.findByRole("combobox");
    const body = within(document.body);
    await userEvent.click(select);
    await expect(
      body.getAllByRole("option").map((o) => o.textContent),
    ).toEqual(["None", "house v2 (default)", "house v1"]);

    await userEvent.click(body.getByRole("option", { name: "house v1" }));
    await waitFor(() => expect(canvas.getByRole("tabpanel")).toHaveTextContent("> Be brief."));

    await userEvent.click(select);
    await userEvent.click(body.getByRole("option", { name: "None" }));
    await waitFor(() => expect(canvas.getByRole("tabpanel")).not.toHaveTextContent("Fleet instructions"));
    await expect(canvas.queryByTestId("fleet-section")).toBeNull();

    await expect(requested).toEqual(["id-2", "id-1", null]);
  },
};

export const FileTabs: Story = {
  beforeEach: () => stubBriefings(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    const tabs = await canvas.findAllByRole("tab");
    await expect(tabs.map((t) => t.textContent)).toEqual(["Headless run", "pi"]);
    await expect(tabs[0]).toHaveAttribute("aria-selected", "true");

    await userEvent.click(canvas.getByRole("tab", { name: "pi" }));
    await expect(canvas.getByRole("tabpanel")).toHaveTextContent("pi briefing");
    // Switching files is local: nothing more is fetched.
    await expect(requested).toEqual(["id-2"]);
  },
};

export const OwnerTextHighlighted: Story = {
  beforeEach: () => stubBriefings(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    const section = await canvas.findByTestId("fleet-section");
    await expect(section).toHaveTextContent("The protocol takes precedence.");
    // Only the owners' quoted lines are marked, not the wrapper around them.
    const marked = [...section.querySelectorAll("mark")].map((m) => m.textContent);
    await expect(marked).toEqual(["> Comment a plan first.\n", ">\n", "> Then stop.\n"]);
  },
};

export const Loading: Story = {
  beforeEach: () => stubBriefings(() => new Promise<BriefingFile[]>(() => {})),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    await expect(await canvas.findByText("Loading the briefing…")).toBeVisible();
    await expect(canvas.getByRole("combobox")).toBeEnabled();
    await expect(canvas.queryByRole("tablist")).toBeNull();
  },
};

export const LoadError: Story = {
  beforeEach: () =>
    stubBriefings(async () => {
      throw new Error("boom");
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Failed to load the briefing.");
    await expect(canvas.queryByRole("tablist")).toBeNull();
    await expect(requested).toEqual(["id-2"]);
  },
};

// On a phone the trigger is a finger-sized target and the open list stays
// inside the screen.
export const PhoneChoosingVersion: Story = {
  beforeEach: () => stubBriefings(),
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: TOGGLE }));
    const trigger = await canvas.findByRole("combobox", { name: "Fleet instructions shown" });
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    await expect(trigger.closest(".dropdown__trigger")!.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    await userEvent.click(trigger);
    const list = within(document.body).getByRole("listbox");
    const box = list.getBoundingClientRect();
    await expect(box.left).toBeGreaterThanOrEqual(0);
    await expect(box.right).toBeLessThanOrEqual(window.innerWidth);
    await expect(box.bottom).toBeLessThanOrEqual(window.innerHeight);
    for (const option of within(list).getAllByRole("option")) await expect(option).toBeVisible();
  },
};
