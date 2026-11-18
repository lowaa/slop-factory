import { useId, useRef, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { ACCEPT, formatBytes } from "./attachment_rules";
import type { AttachmentUploader, PendingAttachment } from "./use_attachment_uploader";
import "./attachment_field.scss";

interface AttachmentFieldProps {
  uploader: AttachmentUploader;
  // The box the pasted or dropped files are for. Paste events bubble up to
  // this field from it, so it needs nothing wired.
  children: ReactNode;
  disabled?: boolean;
  className?: string;
}

function statusText(item: PendingAttachment): string {
  if (item.error) return item.error;
  if (item.status === "uploading") return `Uploading ${Math.round(item.progress * 100)}%`;
  if (item.status === "done") return "Uploaded";
  return formatBytes(item.file.size);
}

// An uploaded file stays on the ticket whatever the form does next, so it is
// not removable here: the gallery's Delete is what takes it off.
function PendingItem({ item, onRemove, disabled }: { item: PendingAttachment; onRemove: () => void; disabled: boolean }) {
  return (
    <li className={`attachment-field__item attachment-field__item--${item.status}`}>
      <div className="attachment-field__thumb" aria-hidden="true">
        {item.previewUrl ? <img src={item.previewUrl} alt="" /> : <span>{item.isVideo ? "Video" : "File"}</span>}
      </div>
      <div className="attachment-field__details">
        <span className="attachment-field__name" title={item.file.name}>
          {item.file.name}
        </span>
        {item.status === "uploading" ? (
          <progress max={1} value={item.progress} aria-label={`Uploading ${item.file.name}`} />
        ) : null}
        <span className="attachment-field__status" role={item.error ? "alert" : undefined}>
          {statusText(item)}
        </span>
      </div>
      <Button
        variant="ghost"
        size="sm"
        aria-label={`Remove ${item.file.name}`}
        disabled={disabled || item.status === "uploading" || item.status === "done"}
        onClick={onRemove}
      >
        Remove
      </Button>
    </li>
  );
}

// AttachmentField wraps a text box so images pasted into it, and files
// dropped on it or chosen with its button, are collected for upload. What is
// collected is shown as a strip of thumbnails the person can remove from
// before sending; the owner of the uploader decides when to send them.
export default function AttachmentField({ uploader, children, disabled = false, className }: AttachmentFieldProps) {
  const inputId = useId();
  const input = useRef<HTMLInputElement>(null);
  const { items, dragging } = uploader;
  const busy = disabled || uploader.uploading;
  return (
    <div
      className={`attachment-field${dragging ? " attachment-field--dragging" : ""}${className ? ` ${className}` : ""}`}
      {...uploader.pasteProps}
      {...uploader.dropProps}
    >
      {children}
      {items.length > 0 && (
        <ul className="attachment-field__list" aria-label="Attachments to send">
          {items.map((item) => (
            <PendingItem key={item.id} item={item} disabled={busy} onRemove={() => uploader.remove(item.id)} />
          ))}
        </ul>
      )}
      <div className="attachment-field__actions">
        <input
          id={inputId}
          ref={input}
          className="attachment-field__input"
          type="file"
          multiple
          accept={ACCEPT}
          tabIndex={-1}
          data-native-look="visually hidden: the Attach button opens it"
          aria-label="Choose images or videos to attach"
          onChange={(e) => {
            uploader.add(Array.from(e.target.files ?? []));
            // So choosing the same file again fires a change.
            e.target.value = "";
          }}
        />
        <Button size="sm" disabled={busy} onClick={() => input.current?.click()}>
          Attach image or video
        </Button>
        <span className="attachment-field__hint">or paste a screenshot, or drop a file here</span>
      </div>
    </div>
  );
}
