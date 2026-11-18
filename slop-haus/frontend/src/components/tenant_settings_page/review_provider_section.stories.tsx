import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeSetting, makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import ReviewProviderSection from "./review_provider_section";
import { REVIEW_PROVIDER_CONFIG } from "./review_provider_settings";
import "./tenant_settings_page.scss";

const setting = (string_val: string) =>
  makeSetting({ name: REVIEW_PROVIDER_CONFIG, type: "string", string_val, bool_val: null, is_default: string_val === "" });

const STORED = JSON.stringify({
  primary: { provider: "claude", model: "", reasoning_effort: "high" },
  fallback: { provider: "codex", model: "", reasoning_effort: "" },
  fallback_on: ["usage_limit"],
});

const meta = {
  title: "Settings/ReviewProviderSection",
  component: ReviewProviderSection,
  decorators: [
    (Story) => (
      <div className="tenant-settings">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof ReviewProviderSection>;

export default meta;
type Story = StoryObj<typeof meta>;

const region = async (canvasElement: HTMLElement) => within(await within(canvasElement).findByRole("region", { name: "Review provider" }));

export const NoneSet: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: true })], listSettings: async () => [setting("")] }),
  play: async ({ canvasElement }) => {
    const s = await region(canvasElement);
    await expect(await s.findByRole("combobox", { name: "Primary provider" })).toHaveTextContent("Choose…");
    await expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
  },
};

export const WithFallback: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: true })], listSettings: async () => [setting(STORED)] }),
  play: async ({ canvasElement }) => {
    const s = await region(canvasElement);
    await expect(await s.findByRole("checkbox", { name: "Usage limit" })).toBeChecked();
    await expect(s.getByRole("button", { name: "Clear" })).toBeEnabled();
  },
};

export const RefusedByServer: Story = {
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ is_owner: true })],
      listSettings: async () => [setting(STORED)],
      updateSetting: async () => {
        throw new ApiError(422, "fallback.provider must differ from the primary's");
      },
    }),
  play: async ({ canvasElement }) => {
    const s = await region(canvasElement);
    await userEvent.click(await s.findByRole("checkbox", { name: "Backend error" }));
    await userEvent.click(s.getByRole("button", { name: "Save" }));
    await expect(await s.findByRole("alert")).toHaveTextContent("must differ");
  },
};

export const ReadOnlyForMember: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: false })], listSettings: async () => [setting(STORED)] }),
  play: async ({ canvasElement }) => {
    const s = await region(canvasElement);
    await expect(await s.findByText(/Primary: claude/)).toBeVisible();
    await expect(s.queryByRole("combobox")).toBeNull();
  },
};
