import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ClipboardEvent } from "react";
import { ApiError } from "@/api";
import { makeAttachment } from "@/test/fixtures";
import { useAttachmentUploader } from "./use_attachment_uploader";

function png(name = "a.png", size = 10): File {
  return new File([new Uint8Array(size)], name, { type: "image/png" });
}

// What a paste event looks like to the handler: the clipboard's items.
function pasteOf(items: { kind: "file" | "string"; type: string; file?: File }[]) {
  const preventDefault = vi.fn();
  const event = {
    preventDefault,
    clipboardData: { items: items.map((i) => ({ kind: i.kind, type: i.type, getAsFile: () => i.file ?? null })) },
  } as unknown as ClipboardEvent;
  return { event, preventDefault };
}

describe("useAttachmentUploader", () => {
  it("queues an accepted file and lists a refused one with its reason, never to be sent", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png(), new File(["x"], "a.svg", { type: "image/svg+xml" })]));
    expect(result.current.items.map((i) => [i.file.name, i.status, i.rejected])).toEqual([
      ["a.png", "queued", false],
      ["a.svg", "failed", true],
    ]);

    const upload = vi.fn().mockResolvedValue(makeAttachment());
    let sent = true;
    await act(async () => {
      sent = await result.current.uploadAll(upload);
    });

    expect(upload).toHaveBeenCalledTimes(1);
    expect(sent).toBe(false);
  });

  it("uploads one file at a time with its progress, and keeps what the server accepted", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png("one.png"), png("two.png")]));
    const attachment = makeAttachment({ filename: "one.png" });
    const upload = vi.fn(async (file: File, o: { onProgress: (n: number) => void }) => {
      o.onProgress(0.5);
      return { ...attachment, filename: file.name };
    });

    let sent = false;
    await act(async () => {
      sent = await result.current.uploadAll(upload);
    });

    expect(sent).toBe(true);
    expect(result.current.items.map((i) => i.status)).toEqual(["done", "done"]);
    expect(result.current.uploaded().map((a) => a.filename)).toEqual(["one.png", "two.png"]);
  });

  it("starts the next file only when the one before has finished", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png("one.png"), png("two.png")]));
    const releases: (() => void)[] = [];
    const upload = vi.fn(
      (file: File) =>
        new Promise<ReturnType<typeof makeAttachment>>((resolve) => {
          releases.push(() => resolve(makeAttachment({ filename: file.name })));
        }),
    );

    let done: Promise<boolean> = Promise.resolve(false);
    act(() => {
      done = result.current.uploadAll(upload);
    });
    expect(upload).toHaveBeenCalledTimes(1);
    await act(async () => releases[0]());
    await vi.waitFor(() => expect(upload).toHaveBeenCalledTimes(2));
    await act(async () => releases[1]());
    await expect(done).resolves.toBe(true);
  });

  it("sends a file added while an upload is in flight, rather than reporting success without it", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png("one.png")]));
    const upload = vi.fn(async (file: File) => {
      if (file.name === "one.png") result.current.add([png("late.png")]);
      return makeAttachment({ filename: file.name });
    });

    await act(async () => {
      await result.current.uploadAll(upload);
    });

    expect(upload.mock.calls.map(([f]) => f.name)).toEqual(["one.png", "late.png"]);
  });

  it("on a retry sends only what failed, showing the server's reason meanwhile", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png("one.png"), png("two.png")]));
    const upload = vi
      .fn()
      .mockResolvedValueOnce(makeAttachment())
      .mockRejectedValueOnce(new ApiError(413, "the attachment quota is used up"))
      .mockResolvedValueOnce(makeAttachment());

    let sent = true;
    await act(async () => {
      sent = await result.current.uploadAll(upload);
    });
    expect(sent).toBe(false);
    expect(result.current.items[1]).toMatchObject({ status: "failed", error: "the attachment quota is used up" });

    await act(async () => {
      sent = await result.current.uploadAll(upload);
    });
    expect(sent).toBe(true);
    expect(upload).toHaveBeenCalledTimes(3);
  });

  it("removes a file before it is sent", async () => {
    const { result } = renderHook(() => useAttachmentUploader());
    act(() => result.current.add([png("one.png"), png("two.png")]));
    act(() => result.current.remove(result.current.items[0].id));
    expect(result.current.items.map((i) => i.file.name)).toEqual(["two.png"]);
  });

  describe("paste", () => {
    it("takes an image off the clipboard, names it, and stops the browser pasting it", () => {
      const { result } = renderHook(() => useAttachmentUploader());
      const { event, preventDefault } = pasteOf([{ kind: "file", type: "image/png", file: png("image.png") }]);

      act(() => result.current.pasteProps.onPaste(event));

      expect(preventDefault).toHaveBeenCalled();
      expect(result.current.items).toHaveLength(1);
      expect(result.current.items[0].file.name).toMatch(/^screenshot-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.png$/);
    });

    it("leaves a text paste alone", () => {
      const { result } = renderHook(() => useAttachmentUploader());
      const { event, preventDefault } = pasteOf([{ kind: "string", type: "text/plain" }]);

      act(() => result.current.pasteProps.onPaste(event));

      expect(preventDefault).not.toHaveBeenCalled();
      expect(result.current.items).toEqual([]);
    });

    it("leaves a paste that carries text beside its picture alone", () => {
      const { result } = renderHook(() => useAttachmentUploader());
      const { event, preventDefault } = pasteOf([
        { kind: "string", type: "text/plain" },
        { kind: "file", type: "image/png", file: png() },
      ]);

      act(() => result.current.pasteProps.onPaste(event));

      expect(preventDefault).not.toHaveBeenCalled();
      expect(result.current.items).toEqual([]);
    });
  });

  it("takes dropped files, and shows a drag in progress", () => {
    const { result } = renderHook(() => useAttachmentUploader());
    const target = document.createElement("div");
    const dragEvent = (files: File[]) =>
      ({
        preventDefault: vi.fn(),
        currentTarget: target,
        relatedTarget: null,
        dataTransfer: { types: ["Files"], files },
      }) as never;

    act(() => result.current.dropProps.onDragOver(dragEvent([])));
    expect(result.current.dragging).toBe(true);
    act(() => result.current.dropProps.onDrop(dragEvent([png("dropped.png")])));
    expect(result.current.dragging).toBe(false);
    expect(result.current.items.map((i) => i.file.name)).toEqual(["dropped.png"]);
  });
});
