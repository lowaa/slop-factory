import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import { apiClient } from "@/api";
import preview from "../../.storybook/preview";

// The kit's stories test the checks against the kit's own preview; these run
// the hooks of this app's preview, which no compliant story would ever trip.
const meta = { title: "Test/PreviewHooks", parameters: { layout: "centered" } } satisfies Meta;

export default meta;
type Story = StoryObj<typeof meta>;

type Hook = (context: { canvasElement: HTMLElement; parameters: Record<string, unknown> }) => void;

export const AfterEachRejectsUnstyledControls: Story = {
  // Deliberately unstyled, so the global hook would fail this story itself.
  parameters: { defaultStyle: { skip: "deliberately unstyled: this story tests the preview's hook" } },
  render: () => (
    <button type="button" style={{ all: "revert" }}>
      Plain
    </button>
  ),
  play: async ({ canvasElement }) => {
    const afterEach = preview.afterEach as Hook;
    await expect(() => afterEach({ canvasElement, parameters: {} })).toThrow(/default look[\s\S]*Plain/);
  },
};

export const AfterEachRejectsUnstubbedApiCalls: Story = {
  render: () => <p>No controls</p>,
  play: async ({ canvasElement }) => {
    const afterEach = preview.afterEach as Hook;
    await apiClient.listTenants().catch(() => undefined);
    await expect(() => afterEach({ canvasElement, parameters: {} })).toThrow(/apiClient\.listTenants called without a stub/);
  },
};
