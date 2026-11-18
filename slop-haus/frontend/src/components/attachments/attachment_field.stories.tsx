import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import AttachmentField from "./attachment_field";
import { MAX_IMAGE_BYTES } from "./attachment_rules";
import { useAttachmentUploader, type UploadFn } from "./use_attachment_uploader";
import { makeAttachment } from "@/test/fixtures";

const PNG = Uint8Array.from(
  atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="),
  (c) => c.charCodeAt(0),
);

function png(name = "image.png"): File {
  return new File([PNG], name, { type: "image/png" });
}

function clipboardWith(...items: DataTransferItem[] | File[]): DataTransfer {
  const data = new DataTransfer();
  for (const item of items as File[]) data.items.add(item);
  return data;
}

// A box with the field around it, as the comment box and the new ticket form
// use it. The upload never finishes by itself, so a story can look at the
// progress it shows; `fail` makes it refuse instead.
function Harness({ upload }: { upload?: UploadFn }) {
  const uploader = useAttachmentUploader();
  return (
    <div style={{ maxWidth: 520 }}>
      <AttachmentField uploader={uploader}>
        <textarea aria-label="Box" rows={3} />
      </AttachmentField>
      <button type="button" onClick={() => void uploader.uploadAll(upload ?? (() => new Promise(() => {})))}>
        Send
      </button>
    </div>
  );
}

const meta = {
  title: "Attachments/AttachmentField",
  component: Harness,
  // The box and the Send button are the harness's stand-ins for the host
  // form's own, styled there (comments, new ticket form).
  parameters: { defaultStyle: { skip: "harness stand-ins for the host form's own controls" } },
} satisfies Meta<typeof Harness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Attach image or video" })).toBeEnabled();
    await expect(canvas.queryByRole("list")).toBeNull();
  },
};

// The owner's main workflow: a screenshot taken with the OS tool, pasted
// into the box, with no file saved first.
export const PastedScreenshot: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const box = canvas.getByRole("textbox", { name: "Box" });
    box.focus();
    box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboardWith(png()), bubbles: true, cancelable: true }));

    await expect(await canvas.findByText(/^screenshot-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.png$/)).toBeInTheDocument();
    await expect(canvasElement.querySelector(".attachment-field__thumb img")).not.toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: /^Remove screenshot-/ }));
    await expect(canvas.queryByRole("list")).toBeNull();
  },
};

export const PastedTextIsLeftAlone: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const data = new DataTransfer();
    data.setData("text/plain", "just words");
    const event = new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true });
    canvas.getByRole("textbox", { name: "Box" }).dispatchEvent(event);

    await expect(event.defaultPrevented).toBe(false);
    await expect(canvas.queryByRole("list")).toBeNull();
  },
};

export const Uploading: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    canvas
      .getByRole("textbox", { name: "Box" })
      .dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboardWith(png()), bubbles: true, cancelable: true }));
    await canvas.findByText(/^screenshot-/);
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(canvas.getByRole("progressbar")).toBeInTheDocument());
    await expect(canvas.getByRole("button", { name: "Attach image or video" })).toBeDisabled();
  },
};

export const ServerRefusal: Story = {
  args: { upload: async () => Promise.reject(Object.assign(new Error("the attachment quota is used up"), {})) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    canvas
      .getByRole("textbox", { name: "Box" })
      .dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboardWith(png()), bubbles: true, cancelable: true }));
    await canvas.findByText(/^screenshot-/);
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("The upload failed.");
  },
};

export const RefusedByTheClient: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const big = new File([new Uint8Array(MAX_IMAGE_BYTES + 2 * 1024 * 1024)], "huge.png", { type: "image/png" });
    const svg = new File(["<svg/>"], "logo.svg", { type: "image/svg+xml" });
    const input = canvas.getByLabelText("Choose images or videos to attach");
    await userEvent.upload(input, [big, svg], { applyAccept: false });

    const alerts = await canvas.findAllByRole("alert");
    await expect(alerts[0]).toHaveTextContent("huge.png is 12 MB, over the 10 MB limit for images.");
    await expect(alerts[1]).toHaveTextContent("logo.svg is not supported");
  },
};

export const DroppedFile: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const box = canvas.getByRole("textbox", { name: "Box" });
    const data = clipboardWith(png("dropped.png"));
    box.dispatchEvent(new DragEvent("dragover", { dataTransfer: data, bubbles: true, cancelable: true }));
    await waitFor(() => expect(canvasElement.querySelector(".attachment-field--dragging")).not.toBeNull());
    box.dispatchEvent(new DragEvent("drop", { dataTransfer: data, bubbles: true, cancelable: true }));

    await expect(await canvas.findByText("dropped.png")).toBeInTheDocument();
    await expect(canvasElement.querySelector(".attachment-field--dragging")).toBeNull();
  },
};

export const Done: Story = {
  args: { upload: async (file) => makeAttachment({ filename: file.name }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    canvas
      .getByRole("textbox", { name: "Box" })
      .dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboardWith(png()), bubbles: true, cancelable: true }));
    await canvas.findByText(/^screenshot-/);
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));
    await expect(await canvas.findByText("Uploaded")).toBeInTheDocument();
  },
};
