import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeAgentContext, makeComment, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

// The body, the comment, and the handoff twice: on the main tab and in the
// agent context tab, which stays in the page.
const STRONGS = 4;

const SETTING_OFF = [{ name: "render_markdown", type: "bool", bool_val: false, is_default: false }] as never;

function serve(settings: unknown[] = []) {
  vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, body: "the **body**" }));
  vi.spyOn(apiClient, "listComments").mockResolvedValue([makeComment({ id: 1, body: "a **comment**" })]);
  vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([makeAgentContext({ id: 1, body: "a **handoff**" })]);
  vi.spyOn(apiClient, "listMySettings").mockResolvedValue(settings as never);
}

describe("TicketDetail markdown", () => {
  stubTicketDetailDefaults();

  it("renders the body, comments and handoff as markdown by default", async () => {
    serve();
    const { container } = render(<TicketDetail ticketId={5} />);

    await waitFor(() => expect(container.querySelectorAll("strong")).toHaveLength(STRONGS));
    expect(screen.getByText("body").tagName).toBe("STRONG");
    expect(screen.getByText("comment").tagName).toBe("STRONG");
    expect(screen.getAllByText("handoff")[0].tagName).toBe("STRONG");
  });

  it("shows each as typed, with a copy button each, when the setting is off", async () => {
    serve(SETTING_OFF);
    const { container } = render(<TicketDetail ticketId={5} />);

    expect(await screen.findByText("the **body**")).toBeInTheDocument();
    expect(await screen.findByText("a **comment**")).toBeInTheDocument();
    expect(screen.getAllByText("a **handoff**").length).toBeGreaterThan(0);
    expect(container.querySelector("strong")).toBeNull();
    expect(screen.getAllByRole("button", { name: "Copy" })).toHaveLength(STRONGS - 1) // the closed history entry is not reachable;
  });

  it("flips for the page without saving", async () => {
    serve();
    const save = vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue({} as never);
    const { container } = render(<TicketDetail ticketId={5} />);
    await waitFor(() => expect(container.querySelectorAll("strong")).toHaveLength(STRONGS));

    await userEvent.click(screen.getByRole("button", { name: "Plain" }));
    expect(container.querySelector("strong")).toBeNull();
    expect(screen.getByText("the **body**")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Rendered" }));
    await waitFor(() => expect(container.querySelectorAll("strong")).toHaveLength(STRONGS));
    expect(save).not.toHaveBeenCalled();
  });
});
