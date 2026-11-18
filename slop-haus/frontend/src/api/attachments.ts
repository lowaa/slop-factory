import { ApiError, BASE_URL, client, sessionWriteHeaders, unwrap, withTenant } from "./client";
import type { Attachment } from "./types";

export interface UploadOptions {
  onProgress?: (fraction: number) => void;
  signal?: AbortSignal;
}

// The server's problem+json carries the reason a limit refused the file in
// `detail`; that is what a person is shown.
function failureMessage(xhr: XMLHttpRequest): string {
  try {
    const body = JSON.parse(xhr.responseText) as { detail?: string };
    if (body.detail) return body.detail;
  } catch {
    // Not JSON (a proxy's own refusal): fall through to the status.
  }
  if (xhr.status === 413) return "The file is too large.";
  return `Upload failed (${xhr.status}).`;
}

// An XMLHttpRequest rather than fetch, because fetch cannot report how much
// of a request body has been sent and a 100 MB video needs a progress bar.
// It goes around SessionClient.send, so it carries the CSRF and tenant
// headers by hand and does not retry an expired access token.
function sendFile(url: string, file: File, { onProgress, signal }: UploadOptions): Promise<Attachment> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.withCredentials = true;
    xhr.setRequestHeader("Accept", "application/json");
    for (const [name, value] of Object.entries(sessionWriteHeaders())) xhr.setRequestHeader(name, value);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress?.(e.loaded / e.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(JSON.parse(xhr.responseText) as Attachment);
      } else {
        reject(new ApiError(xhr.status, failureMessage(xhr)));
      }
    };
    xhr.onerror = () => reject(new ApiError(0, "The upload failed: check your connection and try again."));
    xhr.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));
    if (signal) {
      if (signal.aborted) return reject(new DOMException("Upload cancelled", "AbortError"));
      signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }
    const form = new FormData();
    form.append("file", file, file.name);
    xhr.send(form);
  });
}

export const attachmentsApi = {
  async listTicketAttachments(ticketId: number): Promise<Attachment[]> {
    const body = await unwrap(client.GET("/api/tickets/{id}/attachments", { params: { path: { id: ticketId } } }));
    return body.attachments;
  },

  uploadTicketAttachment(ticketId: number, file: File, options: UploadOptions = {}): Promise<Attachment> {
    return sendFile(`${BASE_URL}/api/tickets/${ticketId}/attachments`, file, options);
  },

  // For Remote Control's composer: a file staged in a conversation
  // until it is attached to a ticket.
  uploadConversationAttachment(conversationId: string, file: File, options: UploadOptions = {}): Promise<Attachment> {
    return sendFile(`${BASE_URL}/api/conversations/${encodeURIComponent(conversationId)}/attachments`, file, options);
  },

  async deleteAttachment(id: string): Promise<void> {
    await unwrap(client.DELETE("/api/attachments/{aid}", { params: { path: { aid: id } } }));
  },

  // A URL an <img> or <video> can load: neither can set the tenant header,
  // so it travels in the query, as an event stream's does.
  attachmentUrl(id: string, opts: { download?: boolean } = {}): string {
    return withTenant(`${BASE_URL}/api/attachments/${encodeURIComponent(id)}${opts.download ? "?download=true" : ""}`);
  },
};
