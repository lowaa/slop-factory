import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeFakeAuthApi } from "../test/auth_pages_fixture";
import ActivatePage from "./activate_page";
import { withTokenInUrl } from "../test/token_in_url";

const meta: Meta<typeof ActivatePage> = {
  title: "ActivatePage",
  component: ActivatePage,
};

export default meta;
type Story = StoryObj<typeof ActivatePage>;

export const Success: Story = {
  beforeEach: withTokenInUrl("story-token"),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(await canvas.findByRole("status")).toHaveTextContent("Your account is active.");
  },
};

export const ActivationFails: Story = {
  parameters: {
    authConfig: {
      api: makeFakeAuthApi({
        activate: () => Promise.reject(new Error("This activation link has expired.")),
      }),
    },
  },
  beforeEach: withTokenInUrl("expired-token"),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(await canvas.findByRole("alert")).toHaveTextContent("This activation link has expired.");
  },
};

export const MissingToken: Story = {
  beforeEach: withTokenInUrl(null),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(await canvas.findByRole("alert")).toHaveTextContent("This activation link is missing its token.");
  },
};
