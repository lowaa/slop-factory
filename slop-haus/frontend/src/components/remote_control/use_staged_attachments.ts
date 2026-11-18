import { useCallback, useEffect, useRef } from "react";
import { apiClient } from "@/api";
import { useAttachmentUploader, type AttachmentUploader, type UploadFn } from "@/components/attachments";

export interface StagedAttachments {
  uploader: AttachmentUploader;
  // The ids to send with the message: what has finished uploading.
  ids: () => string[];
  // Whether the files listed allow a send: none still uploading, and none
  // failed or turned away (sending would drop them unseen; removing the chip
  // is the way out).
  ready: boolean;
  // Takes off the chips for what a send carried, leaving any file added
  // while it was in flight.
  sent: (ids: string[]) => void;
}

// useStagedAttachments collects files for the open conversation and uploads
// each as soon as it is added, so Send only has to name them. They stay staged
// on the server until a message files them.
export function useStagedAttachments(conversationId: string): StagedAttachments {
  const uploader = useAttachmentUploader();
  const { items, uploading, uploadAll, remove } = uploader;
  const running = useRef(false);
  const upload = useCallback<UploadFn>(
    (file, options) => apiClient.uploadConversationAttachment(conversationId, file, options),
    [conversationId],
  );
  const queued = items.some((item) => item.status === "queued" && !item.rejected);

  useEffect(() => {
    // The ref also covers the gap before `uploading` shows in a render (and a
    // StrictMode double effect), which would otherwise upload a file twice.
    if (!queued || uploading || running.current) return;
    running.current = true;
    void uploadAll(upload).finally(() => {
      running.current = false;
    });
  }, [queued, uploading, uploadAll, upload]);

  const ids = useCallback(() => uploader.uploaded().map((a) => a.id), [uploader]);
  const sent = useCallback(
    (sentIds: string[]) => {
      items.filter((item) => item.attachment && sentIds.includes(item.attachment.id)).forEach((item) => remove(item.id));
    },
    [items, remove],
  );
  const ready = !uploading && !items.some((item) => item.status === "queued" || item.status === "uploading" || item.status === "failed");
  return { uploader, ids, ready, sent };
}
