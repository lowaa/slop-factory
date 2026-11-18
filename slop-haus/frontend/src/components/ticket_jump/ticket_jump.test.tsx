import { describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import { apiClient, ApiError, type Ticket } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { AuthContext } from "@/context/auth";
import { renderSignedIn, signedInAuth } from "@/test/render_signed_in";
import TicketJump from "./ticket_jump";
import { parseTicketJump, useTicketJump } from "./use_ticket_jump";

function Jump({ text }: { text: string }) {
  return <TicketJump jump={useTicketJump(text)} />;
}

// Typing into the box, as far as the jump is concerned: renderSignedIn's
// rerender keeps the router but not the session.
async function renderTyping(first: string) {
  const { rerender } = await renderSignedIn(<Jump text={first} />);
  return (text: string) =>
    rerender(
      <AuthContext.Provider value={signedInAuth()}>
        <Jump text={text} />
      </AuthContext.Provider>,
    );
}

// A getTicket whose answers the test hands out, in whatever order it likes.
function deferredGetTicket() {
  const pending = new Map<number, { resolve: (t: Ticket) => void; reject: (e: unknown) => void }>();
  vi.spyOn(apiClient, "getTicket").mockImplementation(
    (id) =>
      new Promise<Ticket>((resolve, reject) => {
        pending.set(id, { resolve, reject });
      }),
  );
  return {
    resolve: async (id: number, ticket: Ticket) => {
      await act(async () => pending.get(id)!.resolve(ticket));
    },
  };
}

describe("parseTicketJump", () => {
  it.each([
    ["#85", 85],
    [" #85 ", 85],
    ["#085", 85],
  ])("takes %j as ticket %d", (text, id) => {
    expect(parseTicketJump(text)).toBe(id);
  });

  it.each(["85", "#85 auth", "auth #85", "#", "", "#0", "# 85", "#-1", "#1.5", "#99999999999999999999"])(
    "leaves %j as a plain search",
    (text) => {
      expect(parseTicketJump(text)).toBeNull();
    },
  );
});

describe("TicketJump", () => {
  it("offers the ticket the box names, as a link to it from the board", async () => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 85, title: "Jump to a ticket", status: "in_progress" }));

    await renderSignedIn(<Jump text="#85" />);

    const link = await screen.findByRole("link", { name: /#85.*Jump to a ticket/ });
    expect(apiClient.getTicket).toHaveBeenCalledWith(85);
    expect(link).toHaveTextContent("In progress");
    expect(link.getAttribute("href")).toMatch(/^\/tickets\/85\?/);
    expect(link.getAttribute("href")).toContain("from=board");
  });

  it.each(["done", "parked"] as const)("offers a %s ticket too: a jump is not a filter", async (status) => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 12, title: "Old idea", status }));

    await renderSignedIn(<Jump text="#12" />);

    expect(await screen.findByRole("link", { name: /#12.*Old idea/ })).toBeInTheDocument();
  });

  it("says there is no such ticket on a 404", async () => {
    vi.spyOn(apiClient, "getTicket").mockRejectedValue(new ApiError(404, "ticket not found"));

    await renderSignedIn(<Jump text="#404" />);

    expect(await screen.findByText("No ticket #404")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("offers nothing when the lookup fails for another reason", async () => {
    vi.spyOn(apiClient, "getTicket").mockRejectedValue(new ApiError(500, "database is down"));

    await renderSignedIn(<Jump text="#7" />);

    await vi.waitFor(() => expect(apiClient.getTicket).toHaveBeenCalledWith(7));
    await act(async () => {});
    expect(screen.queryByText(/No ticket/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("does not look anything up for text that is not a ticket number", async () => {
    const getTicket = vi.spyOn(apiClient, "getTicket");

    await renderSignedIn(<Jump text="auth #85" />);

    expect(getTicket).not.toHaveBeenCalled();
  });

  it("never offers #8's answer once the box says #85, whichever answer lands first", async () => {
    const lookups = deferredGetTicket();
    const type = await renderTyping("#8");
    type("#85");

    // #8's answer arriving late, while #85's is still on its way.
    await lookups.resolve(8, makeTicket({ id: 8, title: "Eight" }));
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    await lookups.resolve(85, makeTicket({ id: 85, title: "Eighty-five" }));
    expect(screen.getByRole("link", { name: /#85.*Eighty-five/ })).toBeInTheDocument();
  });

  it("keeps #85's offer when #8's answer lands after it", async () => {
    const lookups = deferredGetTicket();
    const type = await renderTyping("#8");
    type("#85");

    await lookups.resolve(85, makeTicket({ id: 85, title: "Eighty-five" }));
    await lookups.resolve(8, makeTicket({ id: 8, title: "Eight" }));

    expect(screen.getByRole("link", { name: /#85.*Eighty-five/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Eight$/ })).not.toBeInTheDocument();
  });

  it("takes #8's offer away as soon as the box says #85, before #85's answer is in", async () => {
    const lookups = deferredGetTicket();
    const type = await renderTyping("#8");
    await lookups.resolve(8, makeTicket({ id: 8, title: "Eight" }));
    expect(screen.getByRole("link", { name: /#8.*Eight/ })).toBeInTheDocument();

    type("#85");

    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("announces its offer politely", async () => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 3, title: "Three" }));

    await renderSignedIn(<Jump text="#3" />);

    const link = await screen.findByRole("link", { name: /#3.*Three/ });
    expect(link.closest("[aria-live]")).toHaveAttribute("aria-live", "polite");
  });
});
