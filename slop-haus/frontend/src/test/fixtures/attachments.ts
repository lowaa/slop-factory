import type { Attachment } from "@/api";

export function makeAttachment(overrides: Partial<Attachment> = {}): Attachment {
  return {
    id: "a1111111-1111-1111-1111-111111111111",
    ticket_id: 1,
    conversation_id: null,
    filename: "screenshot.png",
    content_type: "image/png",
    size_bytes: 2048,
    uploader_kind: "user",
    uploader_id: null,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}
