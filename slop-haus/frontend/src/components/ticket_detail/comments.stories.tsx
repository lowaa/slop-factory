import { useState, type ComponentProps } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { useAttachmentUploader } from "@/components/attachments";
import { makeComment, makeTicket } from "@/test/fixtures";
import Comments from "./comments";
import type { CommentActions } from "./use_comment_form";
import "./ticket_detail.scss";

type FormOverrides = Partial<Pick<CommentActions, "submit">> & { initialBody?: string };

interface StoryProps extends Omit<ComponentProps<typeof Comments>, "form"> {
  formOverrides?: FormOverrides;
  onPostSettingDiscussion?: (needsDiscussion: boolean) => void;
}

// The form is TicketDetail's state, passed in, so the story holds a real
// textbox value and records what the buttons ask for instead of posting.
function CommentsHarness({ formOverrides, onPostSettingDiscussion, ...props }: StoryProps) {
  const [body, setBody] = useState(formOverrides?.initialBody ?? "");
  const uploader = useAttachmentUploader();
  const form: CommentActions = {
    body,
    uploader,
    setBody,
    submit: formOverrides?.submit ?? { busy: false, error: null, setError: () => {}, run: async () => {} },
    handleAddComment: (e) => e.preventDefault(),
    handleCommentSettingNeedsDiscussion: (v) => onPostSettingDiscussion?.(v),
  };
  return <Comments {...props} form={form} />;
}

const THREAD = [
  makeComment({ id: 1, body: "Started on this.", created_at: "2026-03-01T09:00:00Z" }),
  makeComment({
    id: 2,
    body: "Needs another scope: slop-haus/server/internal/ticket/some_really_long_file_name_without_breaks_handler.go",
    created_at: "2026-03-02T15:30:00Z",
  }),
];

const meta = {
  title: "Ticket detail/Comments",
  component: CommentsHarness,
  parameters: { layout: "padded" },
  args: { ticket: makeTicket({ id: 5 }), comments: [] },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof CommentsHarness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoComments: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("No comments yet.")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Comment & needs discussion" })).toBeDisabled();
  },
};

export const Thread: Story = {
  args: { comments: THREAD },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Started on this.")).toBeInTheDocument();
    await expect(canvas.queryByText("No comments yet.")).not.toBeInTheDocument();
  },
};

export const Composing: Story = {
  args: { comments: THREAD, onPostSettingDiscussion: fn() },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const second = canvas.getByRole("button", { name: "Comment & needs discussion" });
    await expect(second).toBeDisabled();
    await userEvent.type(canvas.getByRole("textbox", { name: "Add a comment" }), "A question");
    await expect(second).toBeEnabled();
    await userEvent.click(second);
    await expect(args.onPostSettingDiscussion).toHaveBeenCalledWith(true);
  },
};

export const ClearsNeedsDiscussion: Story = {
  args: { ticket: makeTicket({ id: 5, needs_discussion: true }), comments: THREAD, formOverrides: { initialBody: "Answered." }, onPostSettingDiscussion: fn() },
  play: async ({ canvasElement, args }) => {
    const clear = within(canvasElement).getByRole("button", { name: "Comment & clear needs discussion" });
    await expect(clear).toBeEnabled();
    await userEvent.click(clear);
    await expect(args.onPostSettingDiscussion).toHaveBeenCalledWith(false);
  },
};

export const Posting: Story = {
  args: {
    comments: THREAD,
    formOverrides: { initialBody: "On its way", submit: { busy: true, error: null, setError: () => {}, run: async () => {} } },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Posting…" })).toBeDisabled();
    await expect(canvas.getByRole("button", { name: "Comment & needs discussion" })).toBeDisabled();
  },
};

export const PostError: Story = {
  args: {
    comments: THREAD,
    formOverrides: {
      initialBody: "This one failed",
      submit: { busy: false, error: "Failed to add comment.", setError: () => {}, run: async () => {} },
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("alert")).toHaveTextContent("Failed to add comment.");
    await expect(canvas.getByRole("textbox", { name: "Add a comment" })).toHaveValue("This one failed");
  },
};

// The owner's main workflow: a screenshot pasted straight into the comment
// box shows as a pending attachment, and text pastes are left alone.
export const PastedScreenshot: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const png = Uint8Array.from(
      atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="),
      (c) => c.charCodeAt(0),
    );
    const data = new DataTransfer();
    data.items.add(new File([png], "image.png", { type: "image/png" }));
    canvas
      .getByRole("textbox", { name: "Add a comment" })
      .dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));

    await expect(await canvas.findByText(/^screenshot-.*\.png$/)).toBeInTheDocument();
  },
};
