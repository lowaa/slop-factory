import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient} from "@/api";
import {} from "@/test/render_signed_in";
import {
  chooseFromDropdown,
  selectedValueOf,
} from "@/test/widgets";
import {  } from "@/components/proposals";
import {
  makeComment,
  makeTicket,
} from "@/test/fixtures";
import {} from "@/shared/names/agent_name";
import {} from "@/shared/names/member_name";
import {
  TicketDetail,
  stubTicketDetailDefaults,
  } from "./ticket_detail_test_support";
describe("TicketDetail header", () => {
  stubTicketDetailDefaults();

  describe("timestamps", () => {
    const T0 = "2026-01-01T00:00:00Z";
    const T1 = "2026-02-01T10:00:00Z";
    const T2 = "2026-02-01T11:00:00Z";

    // What the header shows for one of Created/Updated/Closed, as the exact
    // instant on its <time> -- the rendered text is locale-dependent.
    function shownInstant(term: string): string | null {
      const dt = screen.queryByText(term, { selector: "dt" });
      if (!dt) return null;
      return (
        dt.nextElementSibling
          ?.querySelector("time")
          ?.getAttribute("datetime") ?? null
      );
    }

    beforeEach(() => {
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    });

    it("shows when an open ticket was created and last updated, and no closed time", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, created_at: T0, updated_at: T1 }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Created", { selector: "dt" });
      expect(shownInstant("Created")).toBe(T0);
      expect(shownInstant("Updated")).toBe(T1);
      expect(shownInstant("Closed")).toBeNull();
    });

    it("shows when a finished ticket was closed", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          created_at: T0,
          updated_at: T2,
          closed_at: T2,
        }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Closed", { selector: "dt" });
      expect(shownInstant("Closed")).toBe(T2);
    });

    it("links to the PR a finished ticket was merged through", async () => {
      const pr = "https://example.test/org/repo/pull/7";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "done", pr_url: pr }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("PR", { selector: "dt" });
      expect(screen.getByRole("link", { name: pr })).toHaveAttribute(
        "href",
        pr,
      );
    });

    it("shows the base commit, linked to the PR's repository", async () => {
      const sha = "0123456789abcdef0123456789abcdef01234567";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          pr_url: "https://example.test/org/repo/pull/7",
          base_commit: sha,
        }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Base", { selector: "dt" });
      expect(screen.getByRole("link", { name: "0123456" })).toHaveAttribute(
        "href",
        `https://example.test/org/repo/commit/${sha}`,
      );
    });

    it("shows the base commit unlinked when there is no PR to follow", async () => {
      const sha = "0123456789abcdef0123456789abcdef01234567";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, base_commit: sha }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Base", { selector: "dt" });
      expect(screen.getByText("0123456")).toBeInTheDocument();
      expect(
        screen.queryByRole("link", { name: "0123456" }),
      ).not.toBeInTheDocument();
    });

    it("names the ref the ticket was filed with beside the short hash", async () => {
      const sha = "0123456789abcdef0123456789abcdef01234567";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, base_commit: sha, base_ref: "v1.2.0" }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Base", { selector: "dt" });
      expect(screen.getByText("v1.2.0")).toBeInTheDocument();
      expect(screen.getByText("0123456")).toBeInTheDocument();
    });

    it("shows no Base row for a ticket without one", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5 }));

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Created", { selector: "dt" });
      expect(
        screen.queryByText("Base", { selector: "dt" }),
      ).not.toBeInTheDocument();
    });

    it("shows no PR row for a ticket without one", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5 }));

      render(<TicketDetail ticketId={5} />);

      await screen.findByText("Created", { selector: "dt" });
      expect(
        screen.queryByText("PR", { selector: "dt" }),
      ).not.toBeInTheDocument();
    });

    it("shows when each comment was posted", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5 }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([
        makeComment({ id: 1, body: "first", created_at: T1 }),
        makeComment({ id: 2, body: "second", created_at: T2 }),
      ]);

      render(<TicketDetail ticketId={5} />);

      for (const [body, at] of [
        ["first", T1],
        ["second", T2],
      ]) {
        const item = (await screen.findByText(body)).closest("li")!;
        expect(within(item).getByRole("time")).toHaveAttribute("datetime", at);
      }
    });

    it("picks up the closed and updated times when the status change closes the ticket", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, created_at: T0, updated_at: T0 }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          created_at: T0,
          updated_at: T1,
          closed_at: T1,
        }),
      );

      render(<TicketDetail ticketId={5} />);
      await chooseFromDropdown(user, "Status", "done");

      await screen.findByText("Closed", { selector: "dt" });
      expect(shownInstant("Closed")).toBe(T1);
      expect(shownInstant("Updated")).toBe(T1);
    });

    // An agent may have finished the ticket while the human had it open;
    // the reply to a status change is the server's account of that too.
    it("picks up the PR link with the status a reply brings", async () => {
      const user = userEvent.setup();
      const pr = "https://example.test/org/repo/pull/7";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, updated_at: T0 }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          updated_at: T1,
          closed_at: T1,
          pr_url: pr,
        }),
      );

      render(<TicketDetail ticketId={5} />);
      await chooseFromDropdown(user, "Status", "done");

      expect(await screen.findByRole("link", { name: pr })).toHaveAttribute(
        "href",
        pr,
      );
    });

    it("says an approved ticket that is done has nothing left to merge", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          updated_at: T0,
          closed_at: T0,
          approved_at: T0,
        }),
      );

      render(<TicketDetail ticketId={5} />);

      expect(await screen.findByText(/Approved/)).not.toHaveTextContent(
        "will merge",
      );
    });

    // A reply to a non-status edit is a whole ticket as of its own write;
    // it must not put back a PR link state the status merge has not seen.
    it("keeps the PR link through a reply to a priority change", async () => {
      const user = userEvent.setup();
      const pr = "https://example.test/org/repo/pull/7";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          updated_at: T0,
          closed_at: T0,
          pr_url: pr,
        }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, priority: "high", updated_at: T1, pr_url: null }),
      );

      render(<TicketDetail ticketId={5} />);
      await screen.findByRole("link", { name: pr });
      await chooseFromDropdown(user, "Priority", "high");
      await waitFor(() => expect(shownInstant("Updated")).toBe(T1));

      expect(screen.getByRole("link", { name: pr })).toBeInTheDocument();
    });

    // Replies can land in either order; the slower one is the older write,
    // and the ticket must not look less recently updated because of it.
    it("does not let a slow reply move the updated time backwards", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, updated_at: T0 }),
      );
      let releaseStatus: () => void = () => {};
      vi.spyOn(apiClient, "updateTicket").mockImplementation(
        async (_id, input) => {
          if ("status" in input) {
            return new Promise((resolve) => {
              releaseStatus = () =>
                resolve(
                  makeTicket({
                    id: 5,
                    status: "done",
                    updated_at: T1,
                    closed_at: T1,
                  }),
                );
            });
          }
          return makeTicket({ id: 5, priority: "high", updated_at: T2 });
        },
      );

      render(<TicketDetail ticketId={5} />);
      await chooseFromDropdown(user, "Status", "done");
      await chooseFromDropdown(user, "Priority", "high");
      await waitFor(() => expect(shownInstant("Updated")).toBe(T2));

      await act(async () => {
        releaseStatus();
      });

      expect(shownInstant("Updated")).toBe(T2);
      // The status is still this reply's to settle, and closed_at with it.
      expect(shownInstant("Closed")).toBe(T1);
    });

    it("picks up the updated time when opening moves the ticket out of preparing", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "preparing",
          can_ready: true,
          updated_at: T0,
        }),
      );
      vi.spyOn(apiClient, "markTicketReady").mockResolvedValue(
        makeTicket({ id: 5, status: "open", updated_at: T1 }),
      );

      render(<TicketDetail ticketId={5} />);
      await user.click(await screen.findByRole("button", { name: "Open" }));

      await waitFor(() => expect(shownInstant("Updated")).toBe(T1));
    });

    it("picks up the updated time when the human's answer puts a discussion back in the queue", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket")
        .mockResolvedValueOnce(
          makeTicket({ id: 5, status: "discussion", updated_at: T0 }),
        )
        .mockResolvedValueOnce(
          makeTicket({ id: 5, status: "open", updated_at: T1 }),
        );
      vi.spyOn(apiClient, "addComment").mockResolvedValue(
        makeComment({ id: 9, body: "the answer" }),
      );

      render(<TicketDetail ticketId={5} />);
      await user.type(
        await screen.findByLabelText("Add a comment"),
        "the answer",
      );
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      await waitFor(() => expect(shownInstant("Updated")).toBe(T1));
    });

    // The post-answer refresh is a read, so a status the human sets while it
    // is in flight is newer than anything it reports.
    it("does not let the post-answer refresh undo a status set meanwhile", async () => {
      const user = userEvent.setup();
      let releaseRefresh: () => void = () => {};
      vi.spyOn(apiClient, "getTicket")
        .mockResolvedValueOnce(
          makeTicket({ id: 5, status: "discussion", updated_at: T0 }),
        )
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              releaseRefresh = () =>
                resolve(
                  makeTicket({
                    id: 5,
                    status: "open",
                    updated_at: T1,
                    closed_at: null,
                  }),
                );
            }),
        );
      vi.spyOn(apiClient, "addComment").mockResolvedValue(
        makeComment({ id: 9, body: "the answer" }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "done", updated_at: T2, closed_at: T2 }),
      );

      render(<TicketDetail ticketId={5} />);
      await user.type(
        await screen.findByLabelText("Add a comment"),
        "the answer",
      );
      await user.click(screen.getByRole("button", { name: "Post comment" }));
      await waitFor(() => expect(apiClient.getTicket).toHaveBeenCalledTimes(2));
      await chooseFromDropdown(user, "Status", "done");
      await waitFor(() => expect(shownInstant("Closed")).toBe(T2));

      await act(async () => {
        releaseRefresh();
      });

      expect(selectedValueOf("Status")).toContain("done");
      expect(shownInstant("Closed")).toBe(T2);
      expect(shownInstant("Updated")).toBe(T2);
    });

    // closed_at is the server's account of a status; a reply whose status
    // was superseded must not leave its closed time behind on a reopened
    // ticket.
    it("drops the closed time of a superseded status change", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, updated_at: T0 }),
      );
      let releaseFirst: () => void = () => {};
      let call = 0;
      vi.spyOn(apiClient, "updateTicket").mockImplementation(async () => {
        call += 1;
        if (call === 1) {
          return new Promise((resolve) => {
            releaseFirst = () =>
              resolve(
                makeTicket({
                  id: 5,
                  status: "done",
                  updated_at: T1,
                  closed_at: T1,
                }),
              );
          });
        }
        return makeTicket({
          id: 5,
          status: "open",
          updated_at: T2,
          closed_at: null,
        });
      });

      render(<TicketDetail ticketId={5} />);
      await chooseFromDropdown(user, "Status", "done");
      await chooseFromDropdown(user, "Status", "open");
      await waitFor(() => expect(shownInstant("Updated")).toBe(T2));

      await act(async () => {
        releaseFirst();
      });

      expect(selectedValueOf("Status")).toContain("open");
      expect(shownInstant("Closed")).toBeNull();
      expect(shownInstant("Updated")).toBe(T2);
    });
  });
});
