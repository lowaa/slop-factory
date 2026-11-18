import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeAppWebhook } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AppWebhookStatus from "./app_webhook_status";
import "./github_webhooks_panel.scss";

const meta = {
  title: "Panels/AppWebhookStatus",
  component: AppWebhookStatus,
  // Its styles are the panel's: it is only ever drawn inside it.
  decorators: [
    (Story) => (
      <div className="github-webhooks-panel">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof AppWebhookStatus>;

export default meta;
type Story = StoryObj<typeof meta>;

// The App's own webhook: configured but not yet proven, then proven by Check
// finding it so.
export const AppWebhookAwaitingEvents: Story = {
  beforeEach: () => {
    let state: "awaiting_events" | "receiving" = "awaiting_events";
    stubApi({
      getGitHubAppWebhook: async () => makeAppWebhook({ state }),
      checkGitHubAppWebhook: async () => {
        state = "receiving";
        return { outcome: "unchanged", webhook: makeAppWebhook({ state }) };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/^Not done\. The App's webhook is set/)).toBeVisible();
    await expect(canvas.getByText("Pull request review")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Check" }));
    await expect(await canvas.findByText(/^Receiving\./)).toBeVisible();
  },
};

export const AppWebhookSharedInstallation: Story = {
  beforeEach: () => {
    stubApi({
      getGitHubAppWebhook: async () => makeAppWebhook({ state: "shared_installation", configured_url: "" }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/Another workspace on this server already receives/)).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Rotate secret" })).toBeNull();
  },
};
