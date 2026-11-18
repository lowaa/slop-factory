import { describe, expect, it, vi } from "vitest";
import { waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile } from "@/test/fixtures";
import { chooseFromDropdown } from "@/test/widgets";
import { renderApp, mockBoardShell } from "@/test/render_app";

describe("blocked tab", () => {
  it("hides parked, cancelled and done by default, and keeps a changed filter in the address", async () => {
    window.history.pushState({}, "", "/blocked");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const graph = vi.spyOn(apiClient, "listBlockedPage").mockResolvedValue({ tickets: [] });
    const user = userEvent.setup();

    renderApp();

    await waitFor(() => expect(graph).toHaveBeenCalledTimes(1));
    const asked = graph.mock.calls[0][0].status as string[];
    expect(asked).not.toContain("parked");
    expect(asked).not.toContain("done");
    expect(window.location.search).toBe("");
    await chooseFromDropdown(user, "Priority", "urgent");
    await waitFor(() => expect(window.location.search).toBe("?priority=urgent"));
    await waitFor(() => expect(graph).toHaveBeenLastCalledWith(expect.objectContaining({ priority: ["urgent"] })));
  });

  it("reads the filter from the address, including a shown status that is hidden by default", async () => {
    window.history.pushState({}, "", "/blocked?q=login&exclude=cancelled,done");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const graph = vi.spyOn(apiClient, "listBlockedPage").mockResolvedValue({ tickets: [] });

    renderApp();

    await waitFor(() => expect(graph).toHaveBeenCalled());
    expect(graph).toHaveBeenLastCalledWith(expect.objectContaining({ q: "login", status: expect.arrayContaining(["parked"]) }));
  });
});
