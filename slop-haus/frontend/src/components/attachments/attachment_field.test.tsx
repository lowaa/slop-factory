import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import AttachmentField from "./attachment_field";
import { useAttachmentUploader, type AttachmentUploader } from "./use_attachment_uploader";

let uploader: AttachmentUploader;

function Harness() {
  uploader = useAttachmentUploader();
  return (
    <AttachmentField uploader={uploader}>
      <textarea aria-label="Box" />
    </AttachmentField>
  );
}

function png(name = "a.png"): File {
  return new File([new Uint8Array(10)], name, { type: "image/png" });
}

describe("AttachmentField", () => {
  it("shows a pasted screenshot as a pending item, which can be removed before sending", async () => {
    render(<Harness />);
    const clipboardData = { items: [{ kind: "file", type: "image/png", getAsFile: () => png("image.png") }] };

    fireEvent.paste(screen.getByRole("textbox", { name: "Box" }), { clipboardData });

    const item = await screen.findByText(/^screenshot-.*\.png$/);
    expect(item).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Remove screenshot-/ }));
    expect(screen.queryByText(/^screenshot-/)).toBeNull();
  });

  it("does not take a text paste", () => {
    render(<Harness />);
    const event = new Event("paste", { bubbles: true, cancelable: true });
    Object.assign(event, { clipboardData: { items: [{ kind: "string", type: "text/plain", getAsFile: () => null }] } });

    screen.getByRole("textbox", { name: "Box" }).dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByRole("list", { name: "Attachments to send" })).toBeNull();
  });

  it("takes a file chosen with the picker", async () => {
    render(<Harness />);
    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png("picked.png"));
    expect(await screen.findByText("picked.png")).toBeInTheDocument();
  });

  it("takes a dropped file", () => {
    render(<Harness />);
    fireEvent.drop(screen.getByRole("textbox", { name: "Box" }), {
      dataTransfer: { types: ["Files"], files: [png("dropped.png")] },
    });
    expect(screen.getByText("dropped.png")).toBeInTheDocument();
  });

  it("says why an unsupported or oversized file was turned away", () => {
    render(<Harness />);
    fireEvent.drop(screen.getByRole("textbox", { name: "Box" }), {
      dataTransfer: { types: ["Files"], files: [new File(["<svg/>"], "x.svg", { type: "image/svg+xml" })] },
    });
    expect(screen.getByRole("alert")).toHaveTextContent("x.svg is not supported");
  });

  it("shows upload progress", async () => {
    render(<Harness />);
    fireEvent.drop(screen.getByRole("textbox", { name: "Box" }), { dataTransfer: { types: ["Files"], files: [png("up.png")] } });
    let finish: () => void = () => {};
    const upload = vi.fn(
      (_file: File, o: { onProgress: (n: number) => void }) =>
        new Promise<never>((_, reject) => {
          o.onProgress(0.4);
          finish = () => reject(new Error("stop"));
        }),
    );

    const done = uploader.uploadAll(upload);

    expect(await screen.findByText("Uploading 40%")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Uploading up.png" })).toHaveValue(0.4);
    finish();
    await done;
  });
});
