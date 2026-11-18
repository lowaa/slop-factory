import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeAgentMessage } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AgentMessages from "./agent_messages";

const meta = {
  title: "Agents/AgentMessages",
  component: AgentMessages,
  args: {
    agentId: "11111111-1111-1111-1111-111111111111",
    agentName: "agent-1",
    messages: [],
    composing: false,
    onClose: fn(),
    onSent: fn(),
  },
} satisfies Meta<typeof AgentMessages>;

export default meta;
type Story = StoryObj<typeof meta>;

const thread = [
  makeAgentMessage({ id: 3, body: "Please also run the e2e suite." }),
  makeAgentMessage({
    id: 2,
    kind: "ticket_comment",
    ticket_id: 42,
    state: "delivered",
    delivered_at: "2026-01-01T00:05:00Z",
    body: "Use the shared mixin.\nSecond line.",
  }),
  makeAgentMessage({ id: 1, state: "delivered", delivered_at: "2026-01-01T00:01:00Z" }),
];

const sendAgentMessage = fn(async (_agentId: string, _body: string) => makeAgentMessage());

export const NoMessages: Story = {
  play: async ({ canvasElement }) => {
    // Nothing to compose and nothing sent: the component draws nothing.
    await expect(within(canvasElement).queryByRole("group")).not.toBeInTheDocument();
  },
};

export const Thread: Story = {
  args: { messages: thread },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findAllByRole("listitem")).toHaveLength(3);
    await expect(canvas.getByText(/Comment on #42/)).toBeInTheDocument();
    await expect(canvas.getByText("pending")).toBeInTheDocument();
    await expect(canvas.getByText("Please also run the e2e suite.")).toBeInTheDocument();
    await expect(canvas.getByText(/Use the shared mixin\./)).toBeInTheDocument();
    await expect(canvas.getAllByText(/^delivered/)).toHaveLength(2);
  },
};

export const Composing: Story = {
  args: { composing: true, messages: thread },
  beforeEach: () => {
    sendAgentMessage.mockClear();
    stubApi({ sendAgentMessage });
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const send = await canvas.findByRole("button", { name: "Send" });
    await expect(send).toBeDisabled();
    await userEvent.type(canvas.getByRole("textbox", { name: /Message to agent-1/ }), "hello");
    await expect(send).toBeEnabled();
    await userEvent.click(send);
    await expect(sendAgentMessage).toHaveBeenCalledTimes(1);
    await expect(sendAgentMessage).toHaveBeenCalledWith(args.agentId, "hello");
    await expect(args.onSent).toHaveBeenCalledTimes(1);
    await expect(args.onClose).toHaveBeenCalledTimes(1);
  },
};

export const HintLinksHeldTicket: Story = {
  args: { composing: true, heldTicketId: 42 },
  play: async ({ canvasElement }) => {
    const link = await within(canvasElement).findByRole("link", { name: "comment on it" });
    await expect(link.getAttribute("href")).toContain("/tickets/42");
  },
};

export const HintWithoutTicket: Story = {
  args: { composing: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/comment on it/)).toBeInTheDocument();
    await expect(canvas.queryByRole("link")).not.toBeInTheDocument();
  },
};

export const SendError: Story = {
  args: { composing: true },
  beforeEach: () =>
    stubApi({
      sendAgentMessage: async () => {
        throw new ApiError(429, "Too many messages to this agent.");
      },
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const box = await canvas.findByRole("textbox", { name: /Message to agent-1/ });
    await userEvent.type(box, "hello");
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Too many messages to this agent.");
    // The text is kept so it can be sent again.
    await expect(box).toHaveValue("hello");
    await expect(args.onSent).not.toHaveBeenCalled();
    await expect(args.onClose).not.toHaveBeenCalled();
  },
};
