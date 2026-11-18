import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeAttachment } from "@/test/fixtures";
import AttachmentGallery from "./attachment_gallery";
import type { TicketAttachments } from "./use_ticket_attachments";

function store(over: Partial<TicketAttachments> = {}): TicketAttachments {
  return { attachments: [], loadError: null, deleteError: null, deleting: null, added: vi.fn(), remove: vi.fn(), ...over };
}

const image = makeAttachment({ id: "i1", filename: "board.png" });
const video = makeAttachment({ id: "v1", filename: "demo.mov", content_type: "video/quicktime", size_bytes: 5 * 1024 * 1024 });

describe("AttachmentGallery", () => {
  it("draws nothing for a ticket with no attachments", () => {
    const { container } = render(<AttachmentGallery attachments={store()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows an image as a thumbnail and a video in a player with controls", () => {
    const { container } = render(<AttachmentGallery attachments={store({ attachments: [image, video] })} />);

    expect(screen.getByRole("img", { name: "board.png" })).toHaveAttribute("src", expect.stringContaining("/api/attachments/i1"));
    const player = container.querySelector("video");
    expect(player).toHaveAttribute("controls");
    expect(player).toHaveAttribute("src", expect.stringContaining("/api/attachments/v1"));
  });

  it("says who added each one, when, and how big it is", () => {
    render(
      <AttachmentGallery
        attachments={store({ attachments: [makeAttachment({ ...video, created_at: "2026-03-02T15:30:00Z" })] })}
      />,
    );
    const meta = screen.getByText("demo.mov").parentElement!;
    expect(meta).toHaveTextContent("5 MB");
    expect(meta.querySelector("time")).not.toBeNull();
    expect(meta.querySelector(".attachment-gallery__by")).toHaveTextContent(/\S+ .*· 5 MB/);
  });

  it("opens an image in a lightbox that closes on Escape and on the backdrop", async () => {
    render(<AttachmentGallery attachments={store({ attachments: [image] })} />);

    await userEvent.click(screen.getByRole("button", { name: "Open board.png" }));
    expect(screen.getByRole("dialog", { name: "board.png" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Open board.png" }));
    await userEvent.click(screen.getByRole("dialog"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("deletes through the store", async () => {
    const remove = vi.fn();
    render(<AttachmentGallery attachments={store({ attachments: [image], remove })} />);
    await userEvent.click(screen.getByRole("button", { name: "Delete board.png" }));
    expect(remove).toHaveBeenCalledWith("i1");
  });

  it("reports a refused delete", () => {
    render(<AttachmentGallery attachments={store({ attachments: [image], deleteError: "not found" })} />);
    expect(screen.getByRole("alert")).toHaveTextContent("not found");
  });

  it("reports a failed load", () => {
    render(<AttachmentGallery attachments={store({ loadError: "Failed to load attachments." })} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Failed to load attachments.");
  });
});
