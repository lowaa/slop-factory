import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile, makeTicket } from "@/test/fixtures";
import { chooseFromDropdown } from "@/test/widgets";
import { renderApp, mockBoardShell } from "@/test/render_app";

describe("unreviewed tab", () => {
  it("keeps the filter, and nothing about paging, in the address", async () => {
    // An old link with ?after= starts at the top, with the filter kept.
    window.history.pushState({}, "", "/unreviewed?q=login&from=2026-01-01&after=c9");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const list = vi
      .spyOn(apiClient, "listTicketsPage")
      .mockResolvedValueOnce({ tickets: [makeTicket({ id: 5, title: "Merged", status: "done" })], nextCursor: "c2" })
      .mockResolvedValue({ tickets: [makeTicket({ id: 4, title: "Merged earlier", status: "done" })] });
    const user = userEvent.setup();

    renderApp();

    await screen.findByText("Merged");
    expect(list).toHaveBeenCalledTimes(1);
    expect(list).toHaveBeenLastCalledWith(
      expect.objectContaining({ text: "login", closedFrom: "2026-01-01", cursor: undefined }),
    );

    // Loading more appends and leaves the address alone.
    const before = window.history.length;
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByText("Merged earlier");
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "c2" }));
    expect(window.location.search).not.toContain("c2");
    expect(window.history.length).toBe(before);

    // Applied once typing pauses. The old link's ?after= is not read or
    // written; it is left alone, as any key the filter does not own.
    await user.clear(screen.getByLabelText("Title or text"));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("q")).toBeNull());
    expect(new URLSearchParams(window.location.search).get("from")).toBe("2026-01-01");
    expect(window.history.length).toBe(before);
    // The new filter reaches the server, from the first page.
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ text: "", closedFrom: "2026-01-01", cursor: undefined })),
    );
  });
});

describe("unreviewed tab sort", () => {
  it("keeps the sort in its own key, left out at the page's default", async () => {
    window.history.pushState({}, "", "/unreviewed?sort=priority&q=login");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const list = vi.spyOn(apiClient, "listTicketsPage").mockResolvedValue({ tickets: [makeTicket({ id: 5, title: "Merged", status: "done" })] });
    const user = userEvent.setup();

    renderApp();

    await screen.findByText("Merged");
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "priority", text: "login" }));

    // The default here is recently closed, so choosing it drops the key.
    await chooseFromDropdown(user, "Sort by", "Recently closed or merged");
    await waitFor(() => expect(window.location.search).toBe("?q=login"));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "recently_closed", text: "login" })));
  });
});
