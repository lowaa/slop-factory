import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fx from "@/test/fixtures";
import { describeOperations, type Operation } from "./operations_harness";

const attachment = fx.makeAttachment({ id: "11111111-1111-1111-1111-111111111111" });

const operations: Record<string, Operation> = {
  listTicketAttachments: {
    call: (c) => c.listTicketAttachments(7),
    method: "GET",
    url: "/api/tickets/7/attachments",
    response: { attachments: [attachment] },
    result: [attachment],
  },
  deleteAttachment: {
    call: (c) => c.deleteAttachment(attachment.id),
    method: "DELETE",
    url: `/api/attachments/${attachment.id}`,
  },
};

// The uploads go through XMLHttpRequest, for its progress events, and
// attachmentUrl builds a URL: neither is a fetch.
describeOperations(
  "attachments operations",
  () => import("./attachments").then((m) => m.attachmentsApi),
  operations,
  ["uploadTicketAttachment", "uploadConversationAttachment", "attachmentUrl"],
);

class FakeXhr {
  static last: FakeXhr;
  status = 0;
  responseText = "";
  withCredentials = false;
  headers: Record<string, string> = {};
  method = "";
  url = "";
  body: unknown;
  upload: { onprogress?: (e: { lengthComputable: boolean; loaded: number; total: number }) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  send(body: unknown) {
    this.body = body;
  }
  abort() {
    this.onabort?.();
  }
  reply(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

describe("attachment uploads", () => {
  beforeEach(() => {
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function client() {
    return (await import("./index")).apiClient;
  }

  it("posts the file as the multipart field `file` to the ticket, with credentials", async () => {
    const api = await client();
    const file = new File(["x"], "shot.png", { type: "image/png" });

    const pending = api.uploadTicketAttachment(7, file);
    const xhr = FakeXhr.last;
    xhr.reply(201, attachment);

    await expect(pending).resolves.toEqual(attachment);
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe("/api/tickets/7/attachments");
    expect(xhr.withCredentials).toBe(true);
    expect((xhr.body as FormData).get("file")).toBeInstanceOf(File);
    expect(((xhr.body as FormData).get("file") as File).name).toBe("shot.png");
  });

  it("carries the session's CSRF token and tenant, and names the tenant in a content URL", async () => {
    const api = await client();
    const { session } = await import("./client");
    session.signedIn({ csrf_token: "tok", tenant_id: "t-1" } as never);

    const pending = api.uploadTicketAttachment(7, new File(["x"], "a.png", { type: "image/png" }));
    FakeXhr.last.reply(201, attachment);
    await pending;

    expect(FakeXhr.last.headers).toMatchObject({ "X-CSRF-Token": "tok", "X-Tenant-ID": "t-1" });
    expect(api.attachmentUrl("abc")).toBe("/api/attachments/abc?tenant=t-1");
    expect(api.attachmentUrl("abc", { download: true })).toBe("/api/attachments/abc?download=true&tenant=t-1");
    session.forget();
  });

  it("stages a file in a conversation", async () => {
    const api = await client();
    const pending = api.uploadConversationAttachment("c-1", new File(["x"], "a.png", { type: "image/png" }));
    FakeXhr.last.reply(201, attachment);
    await pending;
    expect(FakeXhr.last.url).toBe("/api/conversations/c-1/attachments");
  });

  it("reports progress as a fraction", async () => {
    const api = await client();
    const onProgress = vi.fn();
    const pending = api.uploadTicketAttachment(7, new File(["x"], "a.png"), { onProgress });
    FakeXhr.last.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 4 });
    FakeXhr.last.reply(201, attachment);
    await pending;
    expect(onProgress).toHaveBeenCalledWith(0.25);
  });

  it("fails with the server's own words for a refused file", async () => {
    const api = await client();
    const pending = api.uploadTicketAttachment(7, new File(["x"], "a.png"));
    FakeXhr.last.reply(413, { detail: "the attachment quota is used up" });
    await expect(pending).rejects.toMatchObject({ status: 413, message: "the attachment quota is used up" });
    await expect(pending).rejects.toMatchObject({ name: "ApiError" });
  });

  it("says so when the network fails", async () => {
    const api = await client();
    const pending = api.uploadTicketAttachment(7, new File(["x"], "a.png"));
    FakeXhr.last.onerror?.();
    await expect(pending).rejects.toMatchObject({ name: "ApiError", status: 0, message: expect.stringContaining("check your connection") });
  });

  it("aborts when its signal does", async () => {
    const api = await client();
    const controller = new AbortController();
    const pending = api.uploadTicketAttachment(7, new File(["x"], "a.png"), { signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

});
