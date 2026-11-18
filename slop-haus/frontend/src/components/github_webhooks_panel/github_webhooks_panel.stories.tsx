import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { type GitHubDelivery } from "@/api";
import { makeAppWebhook, makeGitHubDelivery } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import GitHubWebhooksPanel from "./github_webhooks_panel";

const meta = {
  title: "Panels/GitHubWebhooksPanel",
  component: GitHubWebhooksPanel,
} satisfies Meta<typeof GitHubWebhooksPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

function stubDeliveries(deliveries: GitHubDelivery[], enabled = true) {
  stubApi({
    getGitHubAppWebhook: async () => makeAppWebhook({ state: "unavailable", expected_url: "" }),
    listGitHubWebhooks: async () => ({ enabled, deliveries }),
  });
}

export const NoDeliveries: Story = {
  beforeEach: () => stubDeliveries([]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No deliveries yet.")).toBeVisible();
  },
};

export const WithDeliveries: Story = {
  beforeEach: () =>
    stubDeliveries([
      makeGitHubDelivery({ id: "d1", outcome: "applied", detail: "moved ticket #7 to review", ticket_id: 7 }),
      makeGitHubDelivery({
        id: "d2",
        event: "pull_request_review",
        action: "submitted",
        outcome: "ignored",
        detail: "reviewer has no linked account",
      }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("pull_request.opened")).toBeVisible();
    await expect(canvas.getByText("pull_request_review.submitted")).toBeVisible();
    await expect(canvas.getByText("moved ticket #7 to review")).toBeVisible();
    await expect(canvas.getByRole("link", { name: "#7" })).toBeVisible();
  },
};

export const NotConfigured: Story = {
  beforeEach: () => stubDeliveries([], false),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("GitHub webhooks are not configured on this server.")).toBeVisible();
  },
};

export const DeliveryOutcomes: Story = {
  beforeEach: () =>
    stubDeliveries(
      (["processing", "applied", "ignored", "error"] as const).map((outcome, i) =>
        makeGitHubDelivery({ id: `d${i}`, outcome, detail: `outcome ${outcome}` }),
      ),
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("outcome error");
    for (const outcome of ["processing", "applied", "ignored", "error"]) {
      await expect(canvas.getByText(outcome, { selector: `.github-webhooks-panel__outcome--${outcome}` })).toBeVisible();
      await expect(canvas.getByText(`outcome ${outcome}`)).toBeVisible();
    }
  },
};
