import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { apiClient, type Comment, type Ticket } from "@/api";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

const WITH_PAGE_FIELDS: Partial<Ticket> = {
  id: 5,
  title: "Sort it",
  status: "discussion",
  pr_url: "https://github.com/o/r/pull/9",
  base_commit: "0123456789abcdef",
  closed_at: "2026-01-03T00:00:00Z",
  attempts: [
    makeAttempt({ number: 1, status: "discussion", promoted: true }),
    makeAttempt({ number: 2, status: "in_progress", promoted: false, shadow: true }),
    makeAttempt({ number: 3, status: "review", promoted: false }),
  ],
};

describe("making an attempt unmistakable", () => {
  stubTicketDetailDefaults();

  function load(ticket: Partial<Ticket>, comments: Comment[] = []) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket(ticket));
    vi.spyOn(apiClient, "listComments").mockResolvedValue(comments);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  }

  async function open(attempt?: number) {
    render(<TicketDetail ticketId={5} initialAttempt={attempt} />);
    await screen.findByRole("heading", { name: /#5\b/ });
  }

  describe("the title chip", () => {
    it("names the attempt, and says shadow for a shadow", async () => {
      load(WITH_PAGE_FIELDS);
      await open(2);

      expect(screen.getByText("attempt 2 · shadow")).toHaveClass("attempt-accent-2");
    });

    it("says promoted for the promoted attempt", async () => {
      load(WITH_PAGE_FIELDS);
      await open(1);

      expect(screen.getByText("attempt 1 · promoted")).toBeInTheDocument();
    });

    it("says only the number for an attempt that is neither", async () => {
      load(WITH_PAGE_FIELDS);
      await open(3);

      expect(screen.getByText("attempt 3")).toBeInTheDocument();
    });

    it("is absent on the ticket's own view", async () => {
      load(WITH_PAGE_FIELDS);
      await open();

      expect(screen.queryByText(/^attempt \d/)).not.toBeInTheDocument();
    });

    it("is absent for a single-attempt ticket whose address names an attempt", async () => {
      load({ ...WITH_PAGE_FIELDS, attempts: [makeAttempt({ number: 1 })] });
      await open(1);

      expect(screen.queryByText(/^attempt \d/)).not.toBeInTheDocument();
    });
  });

  describe("the header's fields", () => {
    it("read as the ticket's while an attempt is on show", async () => {
      load(WITH_PAGE_FIELDS);
      await open(2);

      for (const name of ["Ticket PR", "Ticket base", "Ticket closed"]) {
        expect(screen.getByText(name, { selector: "dt" })).toBeInTheDocument();
      }
      expect(screen.queryByText("PR", { selector: "dt" })).not.toBeInTheDocument();
      expect(screen.queryByText("Base", { selector: "dt" })).not.toBeInTheDocument();
    });

    it("keep their plain names on the ticket's own view", async () => {
      load(WITH_PAGE_FIELDS);
      await open();

      for (const name of ["PR", "Base", "Closed"]) {
        expect(screen.getByText(name, { selector: "dt" })).toBeInTheDocument();
      }
      expect(screen.queryByText(/^Ticket (PR|base|closed)$/)).not.toBeInTheDocument();
    });
  });

  describe("the action labels", () => {
    it("name the attempt on its tab: answering while it is in discussion", async () => {
      load(WITH_PAGE_FIELDS);
      await open(1);

      expect(screen.getByRole("button", { name: "Answer attempt 1" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Comment on attempt 1 & needs discussion" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Post comment" })).not.toBeInTheDocument();
    });

    it("name the attempt on its tab: commenting otherwise", async () => {
      load(WITH_PAGE_FIELDS);
      await open(2);

      expect(screen.getByRole("button", { name: "Comment on attempt 2" })).toBeInTheDocument();
    });

    it("name the attempt on a review banner", async () => {
      load(WITH_PAGE_FIELDS);
      await open(3);

      expect(screen.getByRole("button", { name: "Approve attempt 3" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Request changes on attempt 3" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    });

    it("say the ticket's comment reaches every live attempt, on the ticket's view of several", async () => {
      load(WITH_PAGE_FIELDS);
      await open();

      expect(
        screen.getByRole("button", { name: "Comment on the ticket (reaches every live attempt)" }),
      ).toBeInTheDocument();
    });

    it("stay as they were on a ticket with one attempt", async () => {
      load({ id: 5, status: "review", attempts: [makeAttempt({ number: 1, status: "review" })] });
      await open();

      expect(screen.getByRole("button", { name: "Post comment" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Request changes" })).toBeInTheDocument();
    });
  });

  describe("the accent", () => {
    it("is on the attempt's section, the tab's link and a dot on every tab, by attempt number", async () => {
      load(WITH_PAGE_FIELDS);
      await open(3);

      const tabs = within(screen.getByRole("navigation", { name: "Attempts" }));
      expect(tabs.getByRole("link", { name: /Attempt 1/ })).toHaveClass("attempt-accent-1");
      expect(tabs.getByRole("link", { name: /Attempt 3/ })).toHaveClass("attempt-accent-3", "is-selected");
      expect(tabs.getByRole("link", { name: "Ticket" })).not.toHaveClass("attempt-accent-1");
      for (const n of [1, 2, 3]) {
        const link = tabs.getByRole("link", { name: new RegExp(`Attempt ${n}`) });
        expect(link.querySelectorAll(".attempt-tabs__dot")).toHaveLength(1);
      }
      expect(tabs.getByRole("link", { name: "Ticket" }).querySelector(".attempt-tabs__dot")).toBeNull();
      expect(screen.getByRole("heading", { name: "Attempt 3" }).parentElement).toHaveClass("attempt-accent-3");
    });
  });

  describe("the browser tab's title", () => {
    it("names the attempt when one is on show", async () => {
      load(WITH_PAGE_FIELDS);
      await open(3);

      await waitFor(() => expect(document.title).toBe("#5 · attempt 3 – slop-haus"));
    });

    it("names the ticket otherwise", async () => {
      load(WITH_PAGE_FIELDS);
      await open();

      await waitFor(() => expect(document.title).toBe("#5 Sort it – slop-haus"));
    });

    it("is put back when the page goes", async () => {
      document.title = "slop-haus";
      load(WITH_PAGE_FIELDS);
      const view = render(<TicketDetail ticketId={5} />);
      await waitFor(() => expect(document.title).toBe("#5 Sort it – slop-haus"));

      view.unmount();

      expect(document.title).toBe("slop-haus");
    });
  });
});
