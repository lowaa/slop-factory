import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { makeAttachment, makeAgent, makeAgentContext, makeComment, makeMember, makeTicket } from "@/test/fixtures";
import { AgentNamesProvider } from "@/shared/names/agent_name";
import { MemberNamesProvider } from "@/shared/names/member_name";
import { TicketDetail, CURRENT_USER_ID, stubTicketDetailDefaults, deferred, load, switchTo, noAlert } from "./ticket_detail_test_support";

describe("Comments", () => {
  stubTicketDetailDefaults();

  it("renders the ticket's comments once loaded", async () => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, title: "Wire up the board" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([
      makeComment({ id: 1, body: "Started on this", author_user_id: CURRENT_USER_ID }),
      makeComment({ id: 2, body: "Needs another scope", author_agent_id: "11111111-1111-1111-1111-111111111111" }),
    ]);
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: "11111111-1111-1111-1111-111111111111", name: "builder-1" }),
    ]);

    render(
      <AgentNamesProvider>
        <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
          <TicketDetail ticketId={5} />
        </MemberNamesProvider>
      </AgentNamesProvider>,
    );

    expect(await screen.findByText("Started on this")).toBeInTheDocument();
    expect(screen.getByText("Needs another scope")).toBeInTheDocument();
    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    expect(apiClient.getTicket).toHaveBeenCalledWith(5);
    expect(apiClient.listComments).toHaveBeenCalledWith(5);
  });

  // A comment authored by a fellow tenant member (not the signed-in user,
  // not an agent) shows that member's display name -- the whole point of
  // author_user_id existing on Comment now.
  it("shows another member's display name on their comment", async () => {
    const memberId = "66666666-6666-6666-6666-666666666666";
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, title: "Wire up the board" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([
      makeComment({ id: 1, body: "Looks close", author_user_id: memberId }),
    ]);
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({
      members: [makeMember({ id: memberId, display_name: "Grace Hopper" })],
      former: [],
    });

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <TicketDetail ticketId={5} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("Looks close")).toBeInTheDocument();
    expect(await screen.findByText("Grace Hopper")).toBeInTheDocument();
  });

  // A deleted account's comment is still a person's: neither "System" (the
  // server's own notices) nor "someone" (an id we cannot name).
  it("shows 'Deleted user' on a comment whose author's account was deleted", async () => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, title: "Wire up the board" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([
      makeComment({ id: 1, body: "Written before they left", author_user_id: null, author_user_id_deleted: true }),
      makeComment({ id: 2, body: "A server notice" }),
    ]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <TicketDetail ticketId={5} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("Written before they left")).toBeInTheDocument();
    expect(screen.getByText("Deleted user")).toBeInTheDocument();
    expect(screen.getByText("System")).toBeInTheDocument();
    expect(screen.queryByText("someone")).not.toBeInTheDocument();
  });

  it("shows an empty-state message when there are no comments", async () => {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5 }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);

    render(<TicketDetail ticketId={5} />);

    expect(await screen.findByText("No comments yet.")).toBeInTheDocument();
  });

  describe("answering a discussion", () => {
    // The human's comment on a "discussion" ticket is the answer, and the
    // server moves the ticket back to open/blocked as a side effect of the
    // same request -- only a refetch after posting shows that here.
    it("refetches the ticket and its handoffs after the human posts a comment", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket")
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "discussion", resume_agent_id: "agent-42" }))
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "open", resume_agent_id: "agent-42" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([
        makeAgentContext({ id: 1, body: "asking the human" }),
      ]);
      vi.spyOn(apiClient, "addComment").mockResolvedValue(makeComment({ id: 9, body: "here's the answer" }));

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("status");
      await user.type(screen.getByLabelText("Add a comment"), "here's the answer");
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      expect(await screen.findByText("here's the answer")).toBeInTheDocument();
      await expect.poll(() => screen.queryByRole("status")).toBeNull();
      expect(apiClient.getTicket).toHaveBeenCalledTimes(2);
      expect(apiClient.listAgentContexts).toHaveBeenCalledTimes(2);
    });

    // The refresh is for what the comment changed. A priority edited while
    // it was in flight must survive the refresh's older snapshot.
    it("does not let the post-comment refresh revert an edit made meanwhile", async () => {
      const user = userEvent.setup();
      let answerRefresh: () => void = () => {};
      vi.spyOn(apiClient, "getTicket")
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "discussion", priority: "normal" }))
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              answerRefresh = () => resolve(makeTicket({ id: 5, status: "open", priority: "normal" }));
            }),
        );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
      vi.spyOn(apiClient, "addComment").mockResolvedValue(makeComment({ id: 9, body: "the answer" }));
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "discussion", priority: "urgent" }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("status");
      await user.type(screen.getByLabelText("Add a comment"), "the answer");
      await user.click(screen.getByRole("button", { name: "Post comment" }));
      await screen.findByText("the answer");
      await chooseFromDropdown(user, "Priority", "urgent");
      await expect.poll(() => selectedValueOf("Priority")).toContain("urgent");

      await act(async () => answerRefresh());

      await expect.poll(() => screen.queryByRole("status")).toBeNull();
      expect(selectedValueOf("Priority")).toContain("urgent");
    });

    // A failed refresh must not cost the human the comment they just
    // posted -- it already landed on the server.
    it("keeps the newly posted comment even if the post-comment refresh fails", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket")
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "discussion" }))
        .mockRejectedValueOnce(new ApiError(500, "boom"));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "addComment").mockResolvedValue(makeComment({ id: 9, body: "here's the answer" }));

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("status");
      await user.type(screen.getByLabelText("Add a comment"), "here's the answer");
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      expect(await screen.findByText("here's the answer")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });

  describe("comment and needs discussion", () => {
    const FLAG = "Comment & needs discussion";
    const CLEAR = "Comment & clear needs discussion";

    // One request for both, and the ticket shown as the response has it --
    // here an answered discussion back in the queue, flagged.
    it("posts the comment setting the flag, and shows the ticket the response carries", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "discussion", needs_discussion: false }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      let respond: () => void = () => {};
      vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion").mockImplementation(
        () =>
          new Promise((resolve) => {
            respond = () =>
              resolve({
                comment: makeComment({ id: 9, body: "plan it first" }),
                ticket: makeTicket({ id: 5, status: "blocked", needs_discussion: true }),
              });
          }),
      );
      vi.spyOn(apiClient, "addComment");

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("status");
      expect(screen.queryByRole("button", { name: CLEAR })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: FLAG })).toBeDisabled();
      await user.type(screen.getByLabelText("Add a comment"), "plan it first");
      await user.click(screen.getByRole("button", { name: FLAG }));

      expect(apiClient.addCommentSettingNeedsDiscussion).toHaveBeenCalledWith(5, "plan it first", true, undefined);
      expect(apiClient.addComment).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: FLAG })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Posting…" })).toBeDisabled();

      await act(async () => respond());

      expect(await screen.findByText("plan it first")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: CLEAR })).toBeInTheDocument();
      expect(screen.getByRole("checkbox", { name: "Needs discussion" })).toBeChecked();
      expect(selectedValueOf("Status")).toContain("blocked");
      expect(screen.getByLabelText("Add a comment")).toHaveValue("");
      expect(apiClient.getTicket).toHaveBeenCalledTimes(1);
    });

    it("offers clearing on a flagged ticket, and shows it cleared", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "open", needs_discussion: true }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion").mockResolvedValue({
        comment: makeComment({ id: 9, body: "settled, go build" }),
        ticket: makeTicket({ id: 5, status: "open", needs_discussion: false }),
      });

      render(<TicketDetail ticketId={5} />);

      await user.type(await screen.findByLabelText("Add a comment"), "settled, go build");
      expect(screen.queryByRole("button", { name: FLAG })).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: CLEAR }));

      expect(apiClient.addCommentSettingNeedsDiscussion).toHaveBeenCalledWith(5, "settled, go build", false, undefined);
      expect(await screen.findByText("settled, go build")).toBeInTheDocument();
      expect(screen.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
      expect(screen.getByRole("button", { name: FLAG })).toBeInTheDocument();
    });

    // It is a write of the flag and the status, so a change the human
    // makes to either while it is in flight is the newer one, and its
    // older reply must not put the value back.
    it("does not let its reply undo a flag or status the human changed meanwhile", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "open", needs_discussion: false }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      let respond: () => void = () => {};
      vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion").mockImplementation(
        () =>
          new Promise((resolve) => {
            respond = () =>
              resolve({
                comment: makeComment({ id: 9, body: "plan it first" }),
                ticket: makeTicket({ id: 5, status: "open", needs_discussion: true }),
              });
          }),
      );
      vi.spyOn(apiClient, "updateTicket")
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "open", needs_discussion: true }))
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "open", needs_discussion: false }))
        .mockResolvedValueOnce(makeTicket({ id: 5, status: "cancelled", needs_discussion: false }));

      render(<TicketDetail ticketId={5} />);

      await user.type(await screen.findByLabelText("Add a comment"), "plan it first");
      await user.click(screen.getByRole("button", { name: FLAG }));
      // Checked then unchecked: the PATCH the human made last is for false.
      const flag = screen.getByRole("checkbox", { name: "Needs discussion" });
      await user.click(flag);
      await expect.poll(() => (flag as HTMLInputElement).checked).toBe(true);
      await user.click(flag);
      await expect.poll(() => (flag as HTMLInputElement).checked).toBe(false);
      await chooseFromDropdown(user, "Status", "cancelled");
      await expect.poll(() => selectedValueOf("Status")).toContain("cancelled");

      await act(async () => respond());

      expect(await screen.findByText("plan it first")).toBeInTheDocument();
      expect(screen.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
      expect(selectedValueOf("Status")).toContain("cancelled");
    });

    it("keeps what was typed, and the flag as it was, when the request fails", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "open", needs_discussion: false }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion").mockRejectedValue(new ApiError(404, "ticket gone"));

      render(<TicketDetail ticketId={5} />);

      await user.type(await screen.findByLabelText("Add a comment"), "plan it first");
      await user.click(screen.getByRole("button", { name: FLAG }));

      expect(await screen.findByRole("alert")).toHaveTextContent("ticket gone");
      expect(screen.getByLabelText("Add a comment")).toHaveValue("plan it first");
      expect(screen.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
      expect(screen.getByRole("button", { name: FLAG })).toBeEnabled();
    });
  });

  describe("action state", () => {
    describe("comments", () => {
      const BOX = "Add a comment";

      it("Post comment is disabled and shows its busy label while in flight, along with the flag button", async () => {
        const user = userEvent.setup();
        load({});
        const reply = deferred<ReturnType<typeof makeComment>>();
        vi.spyOn(apiClient, "addComment").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "hello there");
        await user.click(screen.getByRole("button", { name: "Post comment" }));

        expect(screen.getByRole("button", { name: "Posting…" })).toBeDisabled();
        expect(screen.getByRole("button", { name: "Comment & needs discussion" })).toBeDisabled();

        await act(async () => reply.resolve(makeComment({ id: 9, body: "hello there" })));

        expect(await screen.findByText("hello there")).toBeInTheDocument();
        expect(screen.getByLabelText(BOX)).toHaveValue("");
        expect(screen.getByRole("button", { name: "Post comment" })).toBeEnabled();
        expect(screen.queryByRole("button", { name: "Posting…" })).not.toBeInTheDocument();
      });

      it("Post comment shows the server's message with the draft kept, and clears it for a retry", async () => {
        const user = userEvent.setup();
        load({});
        const retry = deferred<ReturnType<typeof makeComment>>();
        vi.spyOn(apiClient, "addComment")
          .mockRejectedValueOnce(new ApiError(500, "comment store is down"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "hello there");
        await user.click(screen.getByRole("button", { name: "Post comment" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("comment store is down");
        expect(screen.getByLabelText(BOX)).toHaveValue("hello there");
        expect(screen.getByRole("button", { name: "Post comment" })).toBeEnabled();

        await user.click(screen.getByRole("button", { name: "Post comment" }));
        noAlert();

        await act(async () => retry.resolve(makeComment({ id: 9, body: "hello there" })));
        noAlert();
        expect(await screen.findByText("hello there")).toBeInTheDocument();
      });

      it("the flag button clears the server's message for a retry", async () => {
        const user = userEvent.setup();
        load({});
        const retry = deferred<{ comment: ReturnType<typeof makeComment>; ticket: ReturnType<typeof makeTicket> }>();
        vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion")
          .mockRejectedValueOnce(new ApiError(500, "comment store is down"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "plan it first");
        await user.click(screen.getByRole("button", { name: "Comment & needs discussion" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("comment store is down");

        await user.click(screen.getByRole("button", { name: "Comment & needs discussion" }));
        noAlert();
        expect(screen.getByRole("button", { name: "Posting…" })).toBeDisabled();

        await act(async () =>
          retry.resolve({
            comment: makeComment({ id: 9, body: "plan it first" }),
            ticket: makeTicket({ id: 5, needs_discussion: true }),
          }),
        );
        noAlert();
        expect(await screen.findByText("plan it first")).toBeInTheDocument();
        expect(screen.getByRole("checkbox", { name: "Needs discussion" })).toBeChecked();
      });

      it("drops the draft and its error when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load({});
        vi.spyOn(apiClient, "addComment").mockRejectedValue(new ApiError(500, "comment store is down"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "meant for #5");
        await user.click(screen.getByRole("button", { name: "Post comment" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);

        expect(screen.getByLabelText(BOX)).toHaveValue("");
        noAlert();
      });

      it("leaves the next ticket alone while, and after, the previous one's comment is in flight", async () => {
        const user = userEvent.setup();
        load({});
        const posted = deferred<ReturnType<typeof makeComment>>();
        vi.spyOn(apiClient, "addComment").mockReturnValue(posted.promise);
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "meant for #5");
        await user.click(screen.getByRole("button", { name: "Post comment" }));
        await switchTo(rerender, 6);

        expect(screen.getByRole("button", { name: "Post comment" })).toBeEnabled();

        await user.type(screen.getByLabelText(BOX), "a draft on #6");
        await act(async () => posted.resolve(makeComment({ id: 9, body: "meant for #5" })));

        expect(screen.queryByText("meant for #5")).not.toBeInTheDocument();
        expect(screen.getByText("No comments yet.")).toBeInTheDocument();
        // The old post's success clears the box it was typed in, not this one.
        expect(screen.getByLabelText(BOX)).toHaveValue("a draft on #6");
        expect(screen.getByRole("button", { name: "Post comment" })).toBeEnabled();
      });

      it("does not report the previous ticket's late comment failure on the next one", async () => {
        const user = userEvent.setup();
        load({});
        const posted = deferred<ReturnType<typeof makeComment>>();
        vi.spyOn(apiClient, "addComment").mockReturnValue(posted.promise);
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await user.type(await screen.findByLabelText(BOX), "meant for #5");
        await user.click(screen.getByRole("button", { name: "Post comment" }));
        await switchTo(rerender, 6);

        await act(async () => posted.reject(new ApiError(500, "comment store is down")));

        noAlert();
      });
    });
  });
  describe("attachments", () => {
    function pasteScreenshot() {
      const file = new File([new Uint8Array(10)], "image.png", { type: "image/png" });
      fireEvent.paste(screen.getByLabelText("Add a comment"), {
        clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
      });
    }

    async function openTicket() {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5 }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "addComment").mockResolvedValue(makeComment({ id: 9, body: "see the screenshot" }));
      render(<TicketDetail ticketId={5} />);
      await screen.findByLabelText("Add a comment");
    }

    it("uploads a pasted screenshot to the ticket, then posts the comment, and shows the attachment", async () => {
      const user = userEvent.setup();
      const order: string[] = [];
      await openTicket();
      vi.spyOn(apiClient, "uploadTicketAttachment").mockImplementation(async (_id, file) => {
        order.push("upload");
        return makeAttachment({ id: "n1", filename: file.name });
      });
      vi.mocked(apiClient.addComment).mockImplementation(async () => {
        order.push("comment");
        return makeComment({ id: 9, body: "see the screenshot" });
      });

      await user.type(screen.getByLabelText("Add a comment"), "see the screenshot");
      pasteScreenshot();
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      expect(await screen.findByRole("img", { name: /^screenshot-.*\.png$/ })).toBeInTheDocument();
      expect(apiClient.uploadTicketAttachment).toHaveBeenCalledWith(5, expect.objectContaining({ name: expect.stringMatching(/^screenshot-/) }), expect.anything());
      expect(order).toEqual(["upload", "comment"]);
    });

    it("posts the comment on a second try, sending only the file that failed", async () => {
      const user = userEvent.setup();
      await openTicket();
      vi.spyOn(apiClient, "uploadTicketAttachment")
        .mockRejectedValueOnce(new ApiError(413, "the attachment quota is used up"))
        .mockResolvedValueOnce(makeAttachment({ id: "n1" }));

      await user.type(screen.getByLabelText("Add a comment"), "see the screenshot");
      pasteScreenshot();
      await user.click(screen.getByRole("button", { name: "Post comment" }));
      await screen.findAllByText("the attachment quota is used up");
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      await waitFor(() => expect(apiClient.addComment).toHaveBeenCalledTimes(1));
      expect(apiClient.uploadTicketAttachment).toHaveBeenCalledTimes(2);
    });

    it("uploads before the combined comment and needs-discussion request too", async () => {
      const user = userEvent.setup();
      const order: string[] = [];
      await openTicket();
      vi.spyOn(apiClient, "uploadTicketAttachment").mockImplementation(async () => {
        order.push("upload");
        return makeAttachment({ id: "n1" });
      });
      vi.spyOn(apiClient, "addCommentSettingNeedsDiscussion").mockImplementation(async () => {
        order.push("comment");
        return { comment: makeComment({ id: 9, body: "q" }), ticket: makeTicket({ id: 5, needs_discussion: true }) };
      });

      await user.type(screen.getByLabelText("Add a comment"), "q");
      pasteScreenshot();
      await user.click(screen.getByRole("button", { name: "Comment & needs discussion" }));

      await waitFor(() => expect(order).toEqual(["upload", "comment"]));
    });

    it("does not post the comment when a file cannot be uploaded, and says why", async () => {
      const user = userEvent.setup();
      await openTicket();
      vi.spyOn(apiClient, "uploadTicketAttachment").mockRejectedValue(new ApiError(413, "the attachment quota is used up"));

      await user.type(screen.getByLabelText("Add a comment"), "see the screenshot");
      pasteScreenshot();
      await user.click(screen.getByRole("button", { name: "Post comment" }));

      expect(await screen.findAllByText("the attachment quota is used up")).not.toHaveLength(0);
      expect(apiClient.addComment).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Add a comment")).toHaveValue("see the screenshot");
    });
  });
});
