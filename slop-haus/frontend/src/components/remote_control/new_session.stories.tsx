import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { makeConductor } from "@/test/fixtures";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { stubApi } from "@/test/story_harness";
import NewSession from "./new_session";

const laptop = makeConductor({ id: "c-1", name: "my-laptop" });

const meta = {
  title: "RemoteControl/NewSession",
  component: NewSession,
  args: { machines: [laptop], conductorId: "c-1", onPick: fn(), onStarted: fn() },
  decorators: [
    (Story) => (
      <UserSettingsProvider>
        <Story />
      </UserSettingsProvider>
    ),
  ],
  beforeEach: () =>
    stubApi({
      listConversations: async () => [],
      listMySettings: async () => [],
    }),
} satisfies Meta<typeof NewSession>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("my-laptop")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Send" })).toBeVisible();
  },
};

export const StartFails: Story = {
  beforeEach: () =>
    stubApi({
      listConversations: async () => [],
      listMySettings: async () => [],
      startConversation: async () => {
        throw new Error("down");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByRole("textbox", { name: "Message" }), "hello");
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));
    await expect(await canvas.findByRole("alert")).toBeVisible();
  },
};
