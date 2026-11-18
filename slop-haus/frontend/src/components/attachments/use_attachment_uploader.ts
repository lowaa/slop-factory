import { useCallback, useEffect, useRef, useState, type ClipboardEvent, type DragEvent } from "react";
import type { Attachment } from "@/api";
import { errorMessage } from "@/lib/use_async_action";
import { fileKind, pastedName, validateFile } from "./attachment_rules";

export type PendingStatus = "queued" | "uploading" | "done" | "failed";

export interface PendingAttachment {
  id: string;
  file: File;
  // An object URL for an image, shown as its thumbnail before the upload
  // starts; null for a video or where the browser cannot make one.
  previewUrl: string | null;
  isVideo: boolean;
  status: PendingStatus;
  progress: number;
  error: string | null;
  // The client turned the file away (type or size): it is never sent, and
  // only removing it clears it.
  rejected: boolean;
  attachment: Attachment | null;
}

export type UploadFn = (file: File, options: { onProgress: (fraction: number) => void }) => Promise<Attachment>;

export interface AttachmentUploader {
  items: PendingAttachment[];
  add: (files: File[]) => void;
  remove: (id: string) => void;
  clear: () => void;
  // uploadAll sends every file not yet uploaded, one at a time, and says
  // whether every one is now on the server (false while a rejected file is
  // still listed). Files already done are not sent
  // again, so a retry after a failure only sends what is left.
  uploadAll: (upload: UploadFn) => Promise<boolean>;
  // uploaded is what the server has accepted so far.
  uploaded: () => Attachment[];
  uploading: boolean;
  // Attributes for the box that takes pasted images: only image items on the
  // clipboard are taken over, so pasting text is untouched.
  pasteProps: { onPaste: (e: ClipboardEvent) => void };
  // Attributes for the region that takes dropped files.
  dropProps: { onDragOver: (e: DragEvent) => void; onDragLeave: (e: DragEvent) => void; onDrop: (e: DragEvent) => void };
  dragging: boolean;
}

function previewFor(file: File): string | null {
  if (fileKind(file) !== "image" || typeof URL.createObjectURL !== "function") return null;
  return URL.createObjectURL(file);
}

function revoke(item: PendingAttachment) {
  if (item.previewUrl && typeof URL.revokeObjectURL === "function") URL.revokeObjectURL(item.previewUrl);
}

function hasFiles(e: DragEvent): boolean {
  return Array.from(e.dataTransfer?.types ?? []).includes("Files");
}

export function useAttachmentUploader(): AttachmentUploader {
  const [items, setItems] = useState<PendingAttachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [dragging, setDragging] = useState(false);
  // The list as of the last change rather than the last render: uploadAll
  // changes it between awaits and reads it again after them.
  const latest = useRef<PendingAttachment[]>([]);
  const nextId = useRef(0);
  const update = useCallback((change: (prev: PendingAttachment[]) => PendingAttachment[]) => {
    latest.current = change(latest.current);
    setItems(latest.current);
  }, []);

  useEffect(
    () => () => {
      latest.current.forEach(revoke);
    },
    [],
  );

  const patch = useCallback((id: string, change: Partial<PendingAttachment>) => {
    update((prev) => prev.map((item) => (item.id === id ? { ...item, ...change } : item)));
  }, [update]);

  const add = useCallback((files: File[]) => {
    const added = files.map((file): PendingAttachment => {
      const problem = validateFile(file);
      return {
        id: `pending-${nextId.current++}`,
        file,
        previewUrl: problem ? null : previewFor(file),
        isVideo: fileKind(file) === "video",
        status: problem ? "failed" : "queued",
        progress: 0,
        error: problem,
        rejected: problem !== null,
        attachment: null,
      };
    });
    update((prev) => [...prev, ...added]);
  }, [update]);

  const remove = useCallback((id: string) => {
    latest.current.filter((item) => item.id === id).forEach(revoke);
    update((prev) => prev.filter((item) => item.id !== id));
  }, [update]);

  const clear = useCallback(() => {
    latest.current.forEach(revoke);
    update(() => []);
  }, [update]);

  const uploadAll = useCallback(
    async (upload: UploadFn) => {
      setUploading(true);
      let allSent = true;
      // Read again after each upload rather than iterating a snapshot: a file
      // pasted or dropped meanwhile is sent too, not reported sent and then
      // discarded by the caller clearing the list.
      const tried = new Set<string>();
      try {
        for (;;) {
          const item = latest.current.find((i) => !i.rejected && i.status !== "done" && !tried.has(i.id));
          if (!item) break;
          tried.add(item.id);
          patch(item.id, { status: "uploading", progress: 0, error: null });
          try {
            const attachment = await upload(item.file, { onProgress: (progress) => patch(item.id, { progress }) });
            patch(item.id, { status: "done", progress: 1, attachment });
          } catch (err) {
            allSent = false;
            patch(item.id, { status: "failed", error: errorMessage(err, "The upload failed.") });
          }
        }
      } finally {
        setUploading(false);
      }
      // A turned-away file blocks too: sending without it would drop it unseen.
      return allSent && !latest.current.some((item) => item.rejected);
    },
    [patch],
  );

  function onPaste(e: ClipboardEvent) {
    const clipboard = Array.from(e.clipboardData?.items ?? []);
    // Copying cells from a spreadsheet puts a picture of them on the
    // clipboard beside their text; the text is what was meant then.
    if (clipboard.some((item) => item.kind === "string" && item.type === "text/plain")) return;
    const images = clipboard
      .filter((item) => item.kind === "file" && item.type.startsWith("image/"))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file !== null);
    if (images.length === 0) return;
    e.preventDefault();
    const now = new Date();
    add(images.map((file, i) => new File([file], pastedName(file, now, i), { type: file.type })));
  }

  function onDragOver(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    setDragging(true);
  }

  function onDragLeave(e: DragEvent) {
    // Moving onto a child fires leave on the region too.
    if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
    setDragging(false);
  }

  function onDrop(e: DragEvent) {
    setDragging(false);
    if (!hasFiles(e)) return;
    e.preventDefault();
    add(Array.from(e.dataTransfer.files));
  }

  return {
    items,
    add,
    remove,
    clear,
    uploadAll,
    uploaded: () => latest.current.flatMap((item) => (item.attachment ? [item.attachment] : [])),
    uploading,
    pasteProps: { onPaste },
    dropProps: { onDragOver, onDragLeave, onDrop },
    dragging,
  };
}
