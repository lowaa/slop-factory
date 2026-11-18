import { useEffect, useRef, useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Attachment } from "@/api";
import { AuthorName } from "@/components/author_name";
import Timestamp from "@/shared/timestamp/timestamp";
import { formatBytes, mediaKind } from "./attachment_rules";
import type { TicketAttachments } from "./use_ticket_attachments";
import "./attachment_gallery.scss";

function Lightbox({ attachment, onClose }: { attachment: Attachment; onClose: () => void }) {
  const close = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    close.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="attachment-lightbox" role="dialog" aria-modal="true" aria-label={attachment.filename} onClick={onClose}>
      <img
        src={apiClient.attachmentUrl(attachment.id)}
        alt={attachment.filename}
        // The picture is not the backdrop: clicking it must not close.
        onClick={(e) => e.stopPropagation()}
      />
      <button ref={close} type="button" className="attachment-lightbox__close" onClick={onClose}>
        Close
      </button>
    </div>
  );
}

function Media({ attachment, onOpen }: { attachment: Attachment; onOpen: () => void }) {
  const url = apiClient.attachmentUrl(attachment.id);
  if (mediaKind(attachment.content_type) === "video") {
    return <video className="attachment-gallery__media" controls preload="metadata" src={url} aria-label={attachment.filename} />;
  }
  return (
    <button type="button" className="attachment-gallery__open" aria-label={`Open ${attachment.filename}`} onClick={onOpen}>
      <img className="attachment-gallery__media" src={url} alt={attachment.filename} loading="lazy" />
    </button>
  );
}

interface AttachmentGalleryProps {
  attachments: TicketAttachments;
}

// AttachmentGallery is the ticket page's attachments: images as thumbnails
// that open full size, videos in a player, each with who added it and when.
export default function AttachmentGallery({ attachments: store }: AttachmentGalleryProps) {
  const { attachments, loadError, deleteError, deleting } = store;
  const [open, setOpen] = useState<Attachment | null>(null);
  if (attachments.length === 0 && !loadError) return null;
  return (
    <section className="attachment-gallery">
      <h2>Attachments</h2>
      {loadError && <p role="alert" className="attachment-gallery__notice">{loadError}</p>}
      {deleteError && <p role="alert" className="attachment-gallery__notice">{deleteError}</p>}
      <ul className="attachment-gallery__grid">
        {attachments.map((a) => (
          <li key={a.id} className="attachment-gallery__item">
            <Media attachment={a} onOpen={() => setOpen(a)} />
            <div className="attachment-gallery__meta">
              <span className="attachment-gallery__name" title={a.filename}>
                {a.filename}
              </span>
              <span className="attachment-gallery__by">
                <AuthorName
                  agentId={a.uploader_kind === "agent" ? a.uploader_id : null}
                  userId={a.uploader_kind === "user" ? a.uploader_id : null}
                  userDeleted={a.uploader_kind === "user" && a.uploader_id === null}
                />{" "}
                <Timestamp at={a.created_at} /> · {formatBytes(a.size_bytes)}
              </span>
              <Button
                variant="danger"
                size="sm"
                aria-label={`Delete ${a.filename}`}
                disabled={deleting === a.id}
                onClick={() => void store.remove(a.id)}
              >
                {deleting === a.id ? "Deleting…" : "Delete"}
              </Button>
            </div>
          </li>
        ))}
      </ul>
      {open && <Lightbox attachment={open} onClose={() => setOpen(null)} />}
    </section>
  );
}
