import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { makeAttachment } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AttachmentGallery from "./attachment_gallery";
import type { TicketAttachments } from "./use_ticket_attachments";

// A grey 4:3 picture, so the thumbnails and the lightbox have something to
// draw without a server.
const PICTURE = `data:image/svg+xml,${encodeURIComponent(
  '<svg xmlns="http://www.w3.org/2000/svg" width="640" height="480"><rect width="640" height="480" fill="#6b7a99"/></svg>',
)}`;

function store(over: Partial<TicketAttachments> = {}): TicketAttachments {
  return { attachments: [], loadError: null, deleteError: null, deleting: null, added: fn(), remove: fn(), ...over };
}

const IMAGE = makeAttachment({ id: "i1", filename: "board-wide-screenshot-with-a-very-long-name-that-must-truncate.png" });
const VIDEO = makeAttachment({ id: "v1", filename: "demo.mov", content_type: "video/quicktime", size_bytes: 40 * 1024 * 1024 });

const meta = {
  title: "Attachments/AttachmentGallery",
  component: AttachmentGallery,
  parameters: { layout: "padded" },
  beforeEach: () => stubApi({ attachmentUrl: () => PICTURE }),
} satisfies Meta<typeof AttachmentGallery>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Mixed: Story = {
  args: { attachments: store({ attachments: [IMAGE, makeAttachment({ id: "i2", filename: "two.png" }), VIDEO] }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getAllByRole("img")).toHaveLength(2);
    await expect(canvasElement.querySelector("video")).toHaveAttribute("controls");
  },
};

export const Lightbox: Story = {
  args: { attachments: store({ attachments: [IMAGE] }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: new RegExp(`^Open ${IMAGE.filename}`) }));
    const dialog = within(document.body).getByRole("dialog");
    await expect(dialog).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await expect(within(document.body).queryByRole("dialog")).toBeNull();
  },
};

export const Deleting: Story = {
  args: { attachments: store({ attachments: [IMAGE], deleting: "i1" }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("button", { name: /^Delete/ })).toBeDisabled();
  },
};

export const DeleteRefused: Story = {
  args: { attachments: store({ attachments: [IMAGE], deleteError: "not found" }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("alert")).toHaveTextContent("not found");
  },
};

export const LoadFailed: Story = {
  args: { attachments: store({ loadError: "Failed to load attachments." }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("alert")).toHaveTextContent("Failed to load attachments.");
  },
};
