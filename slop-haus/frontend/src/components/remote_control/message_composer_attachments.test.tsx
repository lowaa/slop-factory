import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { apiClient, ApiError } from "@/api";
import { makeAttachment } from "@/test/fixtures";
import { stubLocalStorage } from "@/test/storage";
import MessageComposer from "./message_composer";
import { useStagedAttachments } from "./use_staged_attachments";

// Mirrors how the conversation view drives the composer.
function Harness({ onSend }: { onSend: (text: string, ids: string[]) => void }) {
  const [value, setValue] = useState("");
  const staged = useStagedAttachments("c-1");
  const sendable = staged.ready && (value.trim() !== "" || staged.uploader.items.length > 0);
  return (
    <MessageComposer
      value={value}
      onChange={setValue}
      onSubmit={() => onSend(value, staged.ids())}
      sendDisabled={!sendable}
      attachments={staged.uploader}
    />
  );
}

const png = (name = "a.png") => new File([new Uint8Array(10)], name, { type: "image/png" });
const box = () => screen.getByRole("textbox", { name: "Message" });
const sendButton = () => screen.getByRole("button", { name: "Send" });

describe("MessageComposer attachments", () => {
  beforeEach(() => {
    stubLocalStorage();
    vi.spyOn(apiClient, "setMyBoolSetting").mockRejectedValue(new Error("retired"));
  });

  it("uploads an image pasted from the clipboard into the conversation and shows it as a chip", async () => {
    const upload = vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "a1" }));
    render(<Harness onSend={vi.fn()} />);

    fireEvent.paste(box(), { clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => png("image.png") }] } });

    expect(await screen.findByText(/^screenshot-.*\.png$/)).toBeInTheDocument();
    await waitFor(() => expect(upload).toHaveBeenCalledWith("c-1", expect.any(File), expect.anything()));
    expect(await screen.findByText("Uploaded")).toBeInTheDocument();
  });

  it("takes a dropped file", async () => {
    const upload = vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "a1" }));
    render(<Harness onSend={vi.fn()} />);

    fireEvent.drop(box(), { dataTransfer: { types: ["Files"], files: [png("dropped.png")] } });

    expect(await screen.findByText("dropped.png")).toBeInTheDocument();
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1));
  });

  it("takes a file chosen with the picker", async () => {
    const upload = vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "a1" }));
    render(<Harness onSend={vi.fn()} />);

    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png("picked.png"));

    expect(await screen.findByText("picked.png")).toBeInTheDocument();
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1));
  });

  it("sends with no text once a file has uploaded, naming its id", async () => {
    vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "a1" }));
    const onSend = vi.fn();
    render(<Harness onSend={onSend} />);
    expect(sendButton()).toBeDisabled();

    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png());
    await waitFor(() => expect(sendButton()).toBeEnabled());
    await userEvent.click(sendButton());

    expect(onSend).toHaveBeenCalledWith("", ["a1"]);
  });

  it("keeps Send off while an upload is in flight", async () => {
    let finish: (a: ReturnType<typeof makeAttachment>) => void = () => {};
    vi.spyOn(apiClient, "uploadConversationAttachment").mockImplementation(() => new Promise((resolve) => (finish = resolve)));
    render(<Harness onSend={vi.fn()} />);
    await userEvent.type(box(), "look");

    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png());

    await waitFor(() => expect(screen.getByText(/^Uploading/)).toBeInTheDocument());
    expect(sendButton()).toBeDisabled();
    finish(makeAttachment({ id: "a1" }));
    await waitFor(() => expect(sendButton()).toBeEnabled());
  });

  it("shows why an upload failed, and Send stays off until the chip is removed", async () => {
    vi.spyOn(apiClient, "uploadConversationAttachment").mockRejectedValue(new ApiError(413, "too big"));
    render(<Harness onSend={vi.fn()} />);
    await userEvent.type(box(), "look");

    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png("big.png"));

    expect(await screen.findByRole("alert")).toHaveTextContent("too big");
    expect(sendButton()).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Remove big.png" }));
    expect(sendButton()).toBeEnabled();
  });

  it("removes a chip that has not uploaded, and nothing is sent for it", async () => {
    vi.spyOn(apiClient, "uploadConversationAttachment").mockRejectedValue(new Error("nope"));
    render(<Harness onSend={vi.fn()} />);

    await userEvent.upload(screen.getByLabelText("Choose images or videos to attach"), png("gone.png"));
    await userEvent.click(await screen.findByRole("button", { name: "Remove gone.png" }));

    expect(screen.queryByText("gone.png")).toBeNull();
  });

  it("offers no attach affordance without an uploader", () => {
    render(<MessageComposer value="" onChange={vi.fn()} onSubmit={vi.fn()} />);

    expect(screen.queryByLabelText("Choose images or videos to attach")).toBeNull();
  });
});
