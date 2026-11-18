import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import type { IntakeSources } from "@/api";
import { makeIntakeDelivery, makeIntakeSource } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import IntakeSourcesPanel from "./intake_sources_panel";
import { chooseOption } from "../../test/choose_option";

const empty: IntakeSources = { enabled: true, hmac: true, sources: [], deliveries: [] };

const meta = {
  title: "Panels/IntakeSourcesPanel",
  component: IntakeSourcesPanel,
  beforeEach: (ctx) => {
    ctx.sources = empty;
    stubApi({ listIntakeSources: async () => ctx.sources });
  },
} satisfies Meta<typeof IntakeSourcesPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoSources: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No sources yet.")).toBeInTheDocument();
    await expect(canvas.getByText("No deliveries yet.")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Create source" })).toBeEnabled();
  },
};

export const NotConfigured: Story = {
  beforeEach: (ctx) => {
    ctx.sources = { ...empty, enabled: false, hmac: false };
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText("The intake door is not configured on this server."),
    ).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Create source" })).toBeDisabled();
    await userEvent.click(canvas.getByRole("combobox", { name: "Credential type" }));
    await expect(canvas.getByRole("option", { name: "hmac" })).toHaveAttribute("aria-disabled", "true");
  },
};

// Each select the form offers, set to every kind it switches between.
export const FormKinds: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("No sources yet.");
    const shows = (label: string, text: string) =>
      expect(canvas.getByRole("combobox", { name: label })).toHaveTextContent(text);

    await chooseOption(canvasElement, "Payload format", "alertmanager");
    await shows("Payload format", "alertmanager");

    const status = canvas.getByRole("combobox", { name: "Default status" });
    await expect(status).toBeDisabled();
    await expect(canvas.getByText("An untrusted source's tickets are always Preparing.")).toBeInTheDocument();
    await chooseOption(canvasElement, "Trust", "trusted");
    await expect(status).toBeEnabled();
    await chooseOption(canvasElement, "Default status", "open");
    await shows("Default status", "open");

    await chooseOption(canvasElement, "Credential type", "hmac");
    await shows("Credential type", "hmac");

    await chooseOption(canvasElement, "Default priority", "high");
    await shows("Default priority", "high");
    await chooseOption(canvasElement, "Max priority", "urgent");
    await shows("Max priority", "urgent");
    await chooseOption(canvasElement, "When a resolved ticket is reported again", "reopen the ticket");
    await shows("When a resolved ticket is reported again", "reopen the ticket");
    await expect(canvas.queryByRole("combobox", { name: "Role match" })).not.toBeInTheDocument();
    await userEvent.type(canvas.getByLabelText("Roles"), "ops");
    await chooseOption(canvasElement, "Role match", "all");
    await shows("Role match", "all");
  },
};

export const WithSourcesAndDeliveries: Story = {
  beforeEach: (ctx) => {
    ctx.sources = {
      ...empty,
      sources: [
        makeIntakeSource({
          id: "s1",
          name: "prom-alerts",
          adapter: "alertmanager",
          trust: "trusted",
          auth: "hmac",
          url: "https://board.example/intake/s1",
          roles: ["ops", "db"],
          role_match: "all",
          paths: ["infra/", "docs/"],
          reopen_policy: "reopen",
          max_priority: "urgent",
          last_delivery_at: "2026-01-02T00:00:00Z",
        }),
        makeIntakeSource({
          id: "s2",
          name: "old-feed",
          url: "https://board.example/intake/s2",
          revoked_at: "2026-01-03T00:00:00Z",
        }),
      ],
      deliveries: [
        makeIntakeDelivery({ id: "d1", source_id: "s1", detail: "opened ticket #7", ticket_id: 7 }),
        makeIntakeDelivery({
          id: "d2",
          source_id: "s1",
          key: "cpu-high",
          outcome: "applied",
          downgraded: true,
          detail: "opened ticket #8",
        }),
      ],
    };
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("https://board.example/intake/s1")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Rotate" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Revoke" })).toBeEnabled();
    await expect(canvas.getByText("Revoked")).toBeInTheDocument();
    await expect(canvas.getByText("downgraded to preparing")).toBeInTheDocument();
    await expect(canvas.getByText("opened ticket #7")).toBeInTheDocument();
  },
};

export const RotatingAndRevoking: Story = {
  beforeEach: (ctx) => {
    ctx.sources = {
      ...empty,
      sources: [makeIntakeSource({ id: "s1", name: "prom-alerts", url: "https://board.example/intake/s1" })],
    };
    ctx.rotate = fn(async () => ({ source: ctx.sources.sources[0], token: "tok-rotated" }));
    ctx.revoke = fn(async () => undefined);
    stubApi({ rotateIntakeSource: ctx.rotate, revokeIntakeSource: ctx.revoke });
  },
  play: async ({ canvasElement, rotate, revoke }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Rotate" }));
    await expect(rotate).toHaveBeenCalledWith("s1");
    await expect(await canvas.findByText("Authorization: Bearer tok-rotated")).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "Revoke" }));
    await expect(revoke).toHaveBeenCalledWith("s1");
  },
};

// Every outcome a delivery can end in, each with its own colour.
export const DeliveryOutcomes: Story = {
  beforeEach: (ctx) => {
    ctx.sources = {
      ...empty,
      sources: [makeIntakeSource({ id: "s1", name: "prom-alerts", url: "https://board.example/intake/s1" })],
      deliveries: (["processing", "applied", "ignored", "rejected", "rate_limited", "error"] as const).map((outcome, i) =>
        makeIntakeDelivery({ id: `d${i}`, source_id: "s1", outcome, detail: `outcome ${outcome}` }),
      ),
    };
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("outcome rate_limited");
    for (const outcome of ["processing", "applied", "ignored", "rejected", "rate_limited", "error"]) {
      await expect(canvas.getByText(outcome, { selector: `.delivery-row__outcome--${outcome}` })).toBeVisible();
      await expect(canvas.getByText(`outcome ${outcome}`)).toBeVisible();
    }
  },
};
