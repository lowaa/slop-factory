import type { Decorator, Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeAgent, makePendingDeviceAuthorization } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { WithAuthPagesConfig } from "@/test/with_auth_pages";
import DevicePage from "./device_page";
import "@/auth_pages_config.scss";

// The page reads ?code= / ?agent= from the real URL, so a story sets it
// and puts the old one back afterwards.
const atUrl = (url: string) => () => {
  const previous = window.location.pathname + window.location.search;
  window.history.replaceState({}, "", url);
  return () => window.history.replaceState({}, "", previous);
};

// The field's look comes from auth_pages_config.scss, scoped under the
// layoutClassName the app's real config gives AuthLayout. The shared test
// config omits both, which would show the field in the browser's default look.
const withAuthPages: Decorator = (Story) => (
  <WithAuthPagesConfig config={{ layoutClassName: "slop-haus-auth" }}>
    <Story />
  </WithAuthPagesConfig>
);

const meta = {
  title: "Pages/DevicePage",
  component: DevicePage,
  decorators: [withAuthPages],
  beforeEach: atUrl("/device"),
} satisfies Meta<typeof DevicePage>;

export default meta;
type Story = StoryObj<typeof meta>;

const pickable = () =>
  stubApi({
    listAgents: async () => [makeAgent({ id: "a1", name: "builder-1" })],
    listConductors: async () => ({ own: [], others: [] }),
  });

export const CodeEntry: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByLabelText("Device code")).toHaveValue("");
    await expect(canvas.getByRole("button", { name: "Look up" })).toBeDisabled();
  },
};

export const UnknownCode: Story = {
  beforeEach: () => {
    stubApi({
      lookupDevice: async () => {
        throw new ApiError(404, "no such pending authorization; it may have expired or been decided already");
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Device code"), "DEAD-BEEF");
    await userEvent.click(canvas.getByRole("button", { name: "Look up" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(/expired or been decided already/);
    await expect(canvas.getByLabelText("Device code")).toHaveValue("DEAD-BEEF");
  },
};

export const ApproveOrDeny: Story = {
  beforeEach: (ctx) => {
    const restoreUrl = atUrl("/device?code=BCDF-GHJK")();
    ctx.decideDevice = fn(async () => {});
    stubApi({ lookupDevice: async () => makePendingDeviceAuthorization(), decideDevice: ctx.decideDevice });
    pickable();
    return restoreUrl;
  },
  play: async ({ canvasElement, decideDevice }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/wants to sign in as one of your agents/)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeDisabled();
    await userEvent.type(canvas.getByLabelText("New agent name"), "builder-y");
    await userEvent.click(canvas.getByRole("button", { name: "Approve" }));
    await expect(decideDevice).toHaveBeenCalledWith("BCDF-GHJK", { approve: true, agent_name: "builder-y", agent_kind: "worker" });
    await expect(await canvas.findByText(/Approved\./)).toBeVisible();
  },
};

export const ForeignNetwork: Story = {
  beforeEach: () => {
    const restoreUrl = atUrl("/device?code=BCDF-GHJK")();
    stubApi({ lookupDevice: async () => makePendingDeviceAuthorization({ from_this_network: false }) });
    pickable();
    return restoreUrl;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(/isn’t this browser’s network/);
  },
};

export const Denied: Story = {
  beforeEach: (ctx) => {
    const restoreUrl = atUrl("/device?code=BCDF-GHJK")();
    ctx.decideDevice = fn(async () => {});
    stubApi({ lookupDevice: async () => makePendingDeviceAuthorization(), decideDevice: ctx.decideDevice });
    pickable();
    return restoreUrl;
  },
  play: async ({ canvasElement, decideDevice }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Deny" }));
    await expect(decideDevice).toHaveBeenCalledWith("BCDF-GHJK", { approve: false });
    await expect(await canvas.findByText(/Denied\./)).toBeVisible();
  },
};
