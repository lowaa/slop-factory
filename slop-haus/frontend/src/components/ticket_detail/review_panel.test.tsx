import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { makeComment, makeTicket } from "@/test/fixtures";
import { MemberNamesProvider } from "@/shared/names/member_name";
import { renderSignedIn, signedInAuth } from "@/test/render_signed_in";
import { AuthContext } from "@/context/auth";
import { TicketDetail, CURRENT_USER_ID, stubTicketDetailDefaults, deferred, load, switchTo, noAlert, describeStatusButtons, statusButtonRows } from "./ticket_detail_test_support";

describe("ReviewPanel", () => {
  stubTicketDetailDefaults();

  describe("review", () => {
    it("shows a banner with a PR link", async () => {
      const pr = "https://example.test/org/repo/pull/9";
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "review", pr_url: pr, resume_agent_id: "agent-42" }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={5} />);

      const banner = await screen.findByRole("status");
      expect(banner).toHaveTextContent("Waiting on your review.");
      expect(banner).toHaveTextContent("agent-42");
      expect(within(banner).getByRole("link", { name: pr })).toHaveAttribute("href", pr);
    });

    it("shows no banner for a ticket that isn't in review", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "open" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
    });

    it("approves the ticket and shows the returned status and the approved note", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const approvedAt = "2026-02-01T10:00:00Z";
      vi.spyOn(apiClient, "approveTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "open", approved_at: approvedAt, approved_by_user_id: CURRENT_USER_ID }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Approve" }));

      expect(apiClient.approveTicket).toHaveBeenCalledWith(5, undefined);
      await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
      expect(await screen.findByText(/Approved/)).toHaveTextContent("the agent will merge it.");
    });

    it("shows the server's error message when approving fails", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "approveTicket").mockRejectedValue(new ApiError(409, "ticket: not in review"));

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Approve" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("ticket: not in review");
      expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    });

    it("requests changes, posting the comment, and shows it in the comments list", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review" }));
      vi.spyOn(apiClient, "listComments")
        .mockResolvedValueOnce([])
        .mockResolvedValueOnce([makeComment({ id: 9, body: "please fix the tests", author_user_id: CURRENT_USER_ID })]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const changesRequestedAt = "2026-02-01T11:00:00Z";
      vi.spyOn(apiClient, "requestTicketChanges").mockResolvedValue(
        makeTicket({ id: 5, status: "open", changes_requested_at: changesRequestedAt }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.type(
        await screen.findByLabelText("What needs to change"),
        "please fix the tests",
      );
      await user.click(screen.getByRole("button", { name: "Request changes" }));

      expect(apiClient.requestTicketChanges).toHaveBeenCalledWith(5, "please fix the tests", undefined);
      expect(await screen.findByText("please fix the tests")).toBeInTheDocument();
      await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
      expect(await screen.findByText(/Changes requested/)).toBeInTheDocument();
    });

    it("does not send a request-changes with an empty comment", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const requestChanges = vi.spyOn(apiClient, "requestTicketChanges");

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Request changes" }));

      expect(requestChanges).not.toHaveBeenCalled();
    });
  });
  describe("post-merge review", () => {
    const pr = "https://example.test/org/repo/pull/12";

    function loadDone(overrides: Parameters<typeof makeTicket>[0] = {}) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, title: "Self-merged", status: "done", pr_url: pr, ...overrides }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    }

    it("offers Mark reviewed on a done ticket nobody approved", async () => {
      loadDone();

      render(<TicketDetail ticketId={5} />);

      const prompt = await screen.findByRole("status");
      expect(prompt).toHaveTextContent("Merged without review.");
      expect(prompt).toHaveTextContent("the PR above");
      expect(screen.getByRole("link", { name: pr })).toHaveAttribute("href", pr);
      expect(screen.getByRole("button", { name: "Mark reviewed" })).toBeInTheDocument();
    });

    it.each([
      ["still open", { status: "open" as const }],
      ["required approval", { requires_approval: true }],
      ["approved", { approved_at: "2026-02-01T10:00:00Z" }],
    ])("does not offer it on a ticket %s", async (_, overrides) => {
      loadDone(overrides);

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByRole("button", { name: "Mark reviewed" })).not.toBeInTheDocument();
    });

    it("marks it reviewed with the comment, and shows who reviewed it", async () => {
      const user = userEvent.setup();
      loadDone();
      vi.spyOn(apiClient, "listComments")
        .mockResolvedValueOnce([])
        .mockResolvedValueOnce([makeComment({ id: 9, body: "Leaks a file handle.", author_user_id: CURRENT_USER_ID })]);
      vi.spyOn(apiClient, "markTicketPostReviewed").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          pr_url: pr,
          post_reviewed_at: "2026-02-02T09:00:00Z",
          post_reviewed_by_user_id: CURRENT_USER_ID,
        }),
      );
      vi.spyOn(apiClient, "listMembers").mockResolvedValue([]);
      vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });

      render(
        <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
          <TicketDetail ticketId={5} />
        </MemberNamesProvider>,
      );

      await user.type(await screen.findByLabelText("Anything you found (optional)"), "  Leaks a file handle.  ");
      await user.click(screen.getByRole("button", { name: "Mark reviewed" }));

      expect(apiClient.markTicketPostReviewed).toHaveBeenCalledWith(5, "Leaks a file handle.");
      expect(await screen.findByText(/Reviewed after merge by/)).toHaveTextContent("you");
      expect(screen.queryByRole("button", { name: "Mark reviewed" })).not.toBeInTheDocument();
      expect(await screen.findByText("Leaks a file handle.")).toBeInTheDocument();
    });

    it("names a post-merge reviewer whose account was deleted as 'Deleted user'", async () => {
      loadDone();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          pr_url: pr,
          post_reviewed_at: "2026-02-02T09:00:00Z",
          post_reviewed_by_user_id: null,
          post_reviewed_by_user_id_deleted: true,
        }),
      );
      vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });

      render(
        <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
          <TicketDetail ticketId={5} />
        </MemberNamesProvider>,
      );

      expect(await screen.findByText(/Reviewed after merge by/)).toHaveTextContent("Deleted user");
    });

    // The response of the click is merged into the loaded ticket; its deleted
    // flag has to come along or the reviewer would read as "someone".
    it("carries a deleted reviewer's flag from the Mark reviewed response", async () => {
      const user = userEvent.setup();
      loadDone();
      vi.spyOn(apiClient, "markTicketPostReviewed").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          pr_url: pr,
          post_reviewed_at: "2026-02-02T09:00:00Z",
          post_reviewed_by_user_id: null,
          post_reviewed_by_user_id_deleted: true,
        }),
      );
      vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });

      render(
        <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
          <TicketDetail ticketId={5} />
        </MemberNamesProvider>,
      );
      await user.click(await screen.findByRole("button", { name: "Mark reviewed" }));

      expect(await screen.findByText(/Reviewed after merge by/)).toHaveTextContent("Deleted user");
    });

    it("does not refetch comments when it posted none", async () => {
      const user = userEvent.setup();
      loadDone();
      vi.spyOn(apiClient, "markTicketPostReviewed").mockResolvedValue(
        makeTicket({ id: 5, status: "done", post_reviewed_at: "2026-02-02T09:00:00Z", post_reviewed_by_user_id: "u-2" }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Mark reviewed" }));

      expect(apiClient.markTicketPostReviewed).toHaveBeenCalledWith(5, "");
      await screen.findByText(/Reviewed after merge by/);
      expect(apiClient.listComments).toHaveBeenCalledTimes(1);
    });

    it("keeps the comment and shows the server's error when marking fails", async () => {
      const user = userEvent.setup();
      loadDone();
      vi.spyOn(apiClient, "markTicketPostReviewed").mockRejectedValue(
        new ApiError(409, "ticket: ticket is already marked reviewed"),
      );

      render(<TicketDetail ticketId={5} />);

      const box = await screen.findByLabelText("Anything you found (optional)");
      await user.type(box, "Looks fine");
      await user.click(screen.getByRole("button", { name: "Mark reviewed" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("already marked reviewed");
      expect(box).toHaveValue("Looks fine");
      expect(screen.getByRole("button", { name: "Mark reviewed" })).toBeEnabled();
    });

    // A status set while the mark is in flight is newer than the mark's
    // reply: taking the ticket out of done clears the mark on the server.
    it("does not show a mark whose reply lands after a newer status change", async () => {
      const user = userEvent.setup();
      loadDone();
      let resolveMark!: (t: ReturnType<typeof makeTicket>) => void;
      vi.spyOn(apiClient, "markTicketPostReviewed").mockReturnValue(new Promise((resolve) => (resolveMark = resolve)));
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(makeTicket({ id: 5, status: "open" }));

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Mark reviewed" }));
      await chooseFromDropdown(user, "Status", "open");
      await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
      await act(async () =>
        resolveMark(
          makeTicket({ id: 5, status: "done", post_reviewed_at: "2026-02-02T09:00:00Z", post_reviewed_by_user_id: "u-2" }),
        ),
      );

      expect(screen.queryByText(/Reviewed after merge by/)).not.toBeInTheDocument();
    });

    it("shows an earlier post-merge review instead of the prompt", async () => {
      loadDone({ post_reviewed_at: "2026-02-02T09:00:00Z", post_reviewed_by_user_id: CURRENT_USER_ID });

      render(<TicketDetail ticketId={5} />);

      expect(await screen.findByText(/Reviewed after merge by/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Mark reviewed" })).not.toBeInTheDocument();
    });

    it("files a follow-up ticket prefilled from this one, and links to it", async () => {
      const user = userEvent.setup();
      loadDone();
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 31 }));

      await renderSignedIn(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "File a follow-up ticket" }));
      expect(screen.getByLabelText("Title")).toHaveValue("Follow-up to #5: ");
      await user.type(screen.getByLabelText("Title"), "close the handle");
      await user.click(screen.getByRole("button", { name: "Preparing" }));

      expect(apiClient.createTicket).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Follow-up to #5: close the handle",
          body: `Found while reviewing #5 Self-merged (${pr}).`,
          status: "preparing",
        }),
      );
      expect(await screen.findByRole("link", { name: "#31" })).toBeInTheDocument();
      expect(screen.getByLabelText("Title")).toHaveValue("Follow-up to #5: ");
    });

    // The other tab is hidden, not unmounted, so a look at the handoffs
    // costs nothing typed on the main tab.
    it("keeps a half-typed follow-up across a look at the other tab", async () => {
      const user = userEvent.setup();
      loadDone();

      await renderSignedIn(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "File a follow-up ticket" }));
      await user.type(screen.getByLabelText("Title"), "close the handle");
      await user.click(screen.getByRole("tab", { name: /Agent context history/ }));
      await user.click(screen.getByRole("tab", { name: "Main" }));

      expect(screen.getByLabelText("Title")).toHaveValue("Follow-up to #5: close the handle");
    });

    it("does not offer a follow-up on a ticket that isn't done", async () => {
      loadDone({ status: "in_progress" });

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByRole("button", { name: "File a follow-up ticket" })).not.toBeInTheDocument();
    });
  });

  describe("action state", () => {
    describeStatusButtons(statusButtonRows("Approve"));

    describe("Request changes", () => {
      const BOX = "What needs to change";

      it("is disabled and shows its busy label while in flight, keeping the draft until it lands", async () => {
        const user = userEvent.setup();
        load({ status: "review" });
        const reply = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "requestTicketChanges").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "tighten the tests");
        await user.click(screen.getByRole("button", { name: "Request changes" }));

        expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled();
        expect(screen.getByLabelText(BOX)).toHaveValue("tighten the tests");

        await act(async () => reply.resolve(makeTicket({ id: 5, status: "open" })));

        await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
        expect(screen.queryByRole("button", { name: "Sending…" })).not.toBeInTheDocument();
      });

      it("shows the server's message with the draft kept, and clears it for a retry", async () => {
        const user = userEvent.setup();
        load({ status: "review" });
        const retry = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "requestTicketChanges")
          .mockRejectedValueOnce(new ApiError(409, "ticket: not in review"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "tighten the tests");
        await user.click(screen.getByRole("button", { name: "Request changes" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("ticket: not in review");
        expect(screen.getByLabelText(BOX)).toHaveValue("tighten the tests");
        expect(screen.getByRole("button", { name: "Request changes" })).toBeEnabled();

        await user.click(screen.getByRole("button", { name: "Request changes" }));
        noAlert();
        expect(apiClient.requestTicketChanges).toHaveBeenLastCalledWith(5, "tighten the tests", undefined);

        await act(async () => retry.resolve(makeTicket({ id: 5, status: "open" })));
        noAlert();
        await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
        expect(screen.queryByLabelText(BOX)).not.toBeInTheDocument();
      });

      it("drops the draft and its error when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load({ status: "review" });
        vi.spyOn(apiClient, "requestTicketChanges").mockRejectedValue(new ApiError(409, "ticket: not in review"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "tighten the tests");
        await user.click(screen.getByRole("button", { name: "Request changes" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);

        expect(screen.getByLabelText(BOX)).toHaveValue("");
        noAlert();
      });
    });

    describe("Mark reviewed", () => {
      const BOX = "Anything you found (optional)";
      const done = { status: "done", requires_approval: false } as const;

      it("is disabled and shows its busy label while in flight, then shows the review it recorded", async () => {
        const user = userEvent.setup();
        load(done);
        const reply = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "markTicketPostReviewed").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await user.click(await screen.findByRole("button", { name: "Mark reviewed" }));
        expect(screen.getByRole("button", { name: "Marking…" })).toBeDisabled();

        await act(async () =>
          reply.resolve(
            makeTicket({ id: 5, ...done, post_reviewed_at: "2026-02-02T09:00:00Z", post_reviewed_by_user_id: "u-2" }),
          ),
        );

        expect(await screen.findByText(/Reviewed after merge by/)).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Marking…" })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Mark reviewed" })).not.toBeInTheDocument();
      });

      it("goes back to normal when the request fails", async () => {
        const user = userEvent.setup();
        load(done);
        const reply = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "markTicketPostReviewed").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await user.click(await screen.findByRole("button", { name: "Mark reviewed" }));
        expect(screen.getByRole("button", { name: "Marking…" })).toBeDisabled();

        await act(async () => reply.reject(new ApiError(500, "ticket: fell over")));

        expect(screen.getByRole("button", { name: "Mark reviewed" })).toBeEnabled();
      });

      it("shows the server's message, and clears it for a retry", async () => {
        const user = userEvent.setup();
        load(done);
        const retry = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "markTicketPostReviewed")
          .mockRejectedValueOnce(new ApiError(409, "ticket: already marked reviewed"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "looks fine");
        await user.click(screen.getByRole("button", { name: "Mark reviewed" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("ticket: already marked reviewed");

        await user.click(screen.getByRole("button", { name: "Mark reviewed" }));
        noAlert();

        await act(async () =>
          retry.resolve(
            makeTicket({ id: 5, ...done, post_reviewed_at: "2026-02-02T09:00:00Z", post_reviewed_by_user_id: "u-2" }),
          ),
        );
        noAlert();
        expect(await screen.findByText(/Reviewed after merge by/)).toBeInTheDocument();
      });

      it("drops the draft and its error when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load(done);
        vi.spyOn(apiClient, "markTicketPostReviewed").mockRejectedValue(new ApiError(409, "ticket: nope"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "looks fine");
        await user.click(screen.getByRole("button", { name: "Mark reviewed" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);

        expect(screen.getByLabelText(BOX)).toHaveValue("");
        noAlert();
      });
    });

    describe("follow-ups", () => {
      it("forgets the tickets filed from the previous ticket when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load({ status: "done", requires_approval: true });
        vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 31 }));
        const { rerender } = await renderSignedIn(<TicketDetail ticketId={5} />);

        await user.click(await screen.findByRole("button", { name: "File a follow-up ticket" }));
        await user.type(screen.getByLabelText("Title"), "close the handle");
        await user.click(screen.getByRole("button", { name: "Preparing" }));
        expect(await screen.findByRole("link", { name: "#31" })).toBeInTheDocument();
        expect(screen.getByText(/Filed/)).toBeInTheDocument();

        // The note's link needs the session, which a rerender must keep supplying.
        rerender(
          <AuthContext.Provider value={signedInAuth()}>
            <TicketDetail ticketId={6} />
          </AuthContext.Provider>,
        );
        await screen.findByRole("heading", { name: /#6\b/ });

        expect(screen.queryByText(/Filed/)).not.toBeInTheDocument();
        expect(screen.queryByRole("link", { name: "#31" })).not.toBeInTheDocument();
      });
    });
  });
});
