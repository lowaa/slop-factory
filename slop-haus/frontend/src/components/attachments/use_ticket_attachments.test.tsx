import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { apiClient, ApiError } from "@/api";
import { makeAttachment } from "@/test/fixtures";
import { useTicketAttachments } from "./use_ticket_attachments";

describe("useTicketAttachments", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("loads the ticket's attachments", async () => {
    vi.spyOn(apiClient, "listTicketAttachments").mockResolvedValue([makeAttachment({ id: "a" })]);

    const { result } = renderHook(() => useTicketAttachments(5));

    await waitFor(() => expect(result.current.attachments.map((a) => a.id)).toEqual(["a"]));
    expect(apiClient.listTicketAttachments).toHaveBeenCalledWith(5);
  });

  it("reports a failed load", async () => {
    vi.spyOn(apiClient, "listTicketAttachments").mockRejectedValue(new ApiError(503, "attachments are not available"));

    const { result } = renderHook(() => useTicketAttachments(5));

    await waitFor(() => expect(result.current.loadError).toBe("attachments are not available"));
  });

  it("keeps an attachment added while the first load was still in flight", async () => {
    let finish: (list: ReturnType<typeof makeAttachment>[]) => void = () => {};
    vi.spyOn(apiClient, "listTicketAttachments").mockReturnValue(new Promise((resolve) => (finish = resolve)));
    const { result } = renderHook(() => useTicketAttachments(5));

    act(() => result.current.added([makeAttachment({ id: "new" })]));
    await act(async () => finish([makeAttachment({ id: "old" })]));

    expect(result.current.attachments.map((a) => a.id).sort()).toEqual(["new", "old"]);
  });

  it("does not list an attachment twice", async () => {
    vi.spyOn(apiClient, "listTicketAttachments").mockResolvedValue([makeAttachment({ id: "a" })]);
    const { result } = renderHook(() => useTicketAttachments(5));
    await waitFor(() => expect(result.current.attachments).toHaveLength(1));

    act(() => result.current.added([makeAttachment({ id: "a" })]));

    expect(result.current.attachments).toHaveLength(1);
  });

  it("deletes through the API and drops it from the list, or reports a refusal and keeps it", async () => {
    vi.spyOn(apiClient, "listTicketAttachments").mockResolvedValue([makeAttachment({ id: "a" })]);
    vi.spyOn(apiClient, "deleteAttachment").mockRejectedValueOnce(new ApiError(404, "not found")).mockResolvedValueOnce(undefined);
    const { result } = renderHook(() => useTicketAttachments(5));
    await waitFor(() => expect(result.current.attachments).toHaveLength(1));

    await act(() => result.current.remove("a"));
    expect(result.current.deleteError).toBe("not found");
    expect(result.current.attachments).toHaveLength(1);

    await act(() => result.current.remove("a"));
    expect(apiClient.deleteAttachment).toHaveBeenCalledWith("a");
    expect(result.current.attachments).toEqual([]);
    expect(result.current.deleteError).toBeNull();
  });
});
