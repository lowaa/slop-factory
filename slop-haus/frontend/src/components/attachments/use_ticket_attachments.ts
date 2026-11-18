import { useCallback, useEffect, useState } from "react";
import { apiClient, type Attachment } from "@/api";
import { errorMessage } from "@/lib/use_async_action";

export interface TicketAttachments {
  attachments: Attachment[];
  loadError: string | null;
  deleteError: string | null;
  deleting: string | null;
  // added puts freshly uploaded attachments in the list without a refetch.
  added: (more: Attachment[]) => void;
  remove: (id: string) => Promise<void>;
}

// useTicketAttachments is the ticket page's list of attachments. A failed
// load is reported beside the section rather than hiding the page.
export function useTicketAttachments(ticketId: number): TicketAttachments {
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiClient
      .listTicketAttachments(ticketId)
      .then((found) => {
        // Keep what an upload added while this was in flight.
        if (!cancelled) setAttachments((prev) => [...found, ...prev.filter((p) => !found.some((f) => f.id === p.id))]);
      })
      .catch((err) => {
        if (!cancelled) setLoadError(errorMessage(err, "Failed to load attachments."));
      });
    return () => {
      cancelled = true;
    };
  }, [ticketId]);

  const added = useCallback((more: Attachment[]) => {
    setAttachments((prev) => [...prev, ...more.filter((m) => !prev.some((p) => p.id === m.id))]);
  }, []);

  const remove = useCallback(async (id: string) => {
    setDeleteError(null);
    setDeleting(id);
    try {
      await apiClient.deleteAttachment(id);
      setAttachments((prev) => prev.filter((a) => a.id !== id));
    } catch (err) {
      setDeleteError(errorMessage(err, "Failed to delete the attachment."));
    } finally {
      setDeleting(null);
    }
  }, []);

  return { attachments, loadError, deleteError, deleting, added, remove };
}
