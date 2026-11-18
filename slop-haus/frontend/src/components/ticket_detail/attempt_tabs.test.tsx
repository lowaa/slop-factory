import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Comment, type Ticket } from "@/api";
import { chooseFromDropdown } from "@/test/widgets";
import {
  makeAgentContext,
  makeAttempt,
  makeComment,
  makeTicket,
} from "@/test/fixtures";
import {
  TicketDetail,
  deferred,
  stubTicketDetailDefaults,
} from "./ticket_detail_test_support";

const FAST_V1 = { id: "v1", name: "fast", version: 1, harness: "pi" as const };
const FAST_V2 = { id: "v2", name: "fast", version: 2, harness: "pi" as const };

const TWO_ATTEMPTS: Partial<Ticket> = {
  id: 5,
  status: "discussion",
  attempts: [
    makeAttempt({ number: 1, status: "discussion" }),
    makeAttempt({
      number: 2,
      status: "discussion",
      promoted: false,
      shadow: true,
      started_revision: 3,
    }),
  ],
};

const COMMENTS: Comment[] = [
  makeComment({ id: 1, body: "The spec, before work started." }),
  makeComment({
    id: 2,
    attempt_number: 1,
    body: "Told attempt one to use a map.",
  }),
  makeComment({
    id: 3,
    attempt_number: 2,
    body: "Told attempt two to use a list.",
  }),
];

describe("a ticket's attempt tabs", () => {
  stubTicketDetailDefaults();

  function load(ticket: Partial<Ticket>, comments: Comment[] = COMMENTS) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket(ticket));
    vi.spyOn(apiClient, "listComments").mockResolvedValue(comments);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  }

  async function open(attempt?: number) {
    render(<TicketDetail ticketId={5} initialAttempt={attempt} />);
    await screen.findByRole("heading", { name: /#5\b/ });
  }

  describe("an attempt's worker cost", () => {
    it("shows the attempt's own figure on its tab, not the ticket's", async () => {
      const figure = (cost_micros: number | null) => ({
        billing_mode: "metered" as const,
        requests: 1,
        input_tokens: 1,
        output_tokens: 1,
        cost_micros,
        unpriced_records: 0,
      });
      const usage = vi.spyOn(apiClient, "getTicketLlmUsage");
      load({
        ...TWO_ATTEMPTS,
        attempts: [
          makeAttempt({
            number: 1,
            status: "discussion",
            worker_cost: [figure(1_500_000)],
          }),
          makeAttempt({
            number: 2,
            status: "discussion",
            promoted: false,
            worker_cost: [figure(20_000)],
          }),
        ],
      });
      const user = userEvent.setup();
      await open(2);

      expect(
        await screen.findByRole("heading", { name: "Attempt 2" }),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Worker cost").nextElementSibling,
      ).toHaveTextContent("$0.02");
      expect(screen.queryByText(/\$1\.50/)).not.toBeInTheDocument();
      expect(usage).not.toHaveBeenCalled();

      await user.click(
        within(screen.getByRole("navigation", { name: "Attempts" })).getByRole(
          "link",
          { name: /Attempt 1/ },
        ),
      );
      expect(
        await screen.findByRole("heading", { name: "Attempt 1" }),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Worker cost").nextElementSibling,
      ).toHaveTextContent("$1.50");
    });
  });

  describe("the tabs", () => {
    it("lists a tab per attempt beside the ticket's own, and the address picks one", async () => {
      load(TWO_ATTEMPTS);
      const user = userEvent.setup();
      await open();

      const nav = screen.getByRole("navigation", { name: "Attempts" });
      expect(
        within(nav)
          .getAllByRole("link")
          .map((l) => l.textContent),
      ).toEqual(["Ticket", "Attempt 1discussion", "Attempt 2discussion"]);
      expect(within(nav).getByRole("link", { name: "Ticket" })).toHaveAttribute(
        "aria-current",
        "page",
      );

      await user.click(within(nav).getByRole("link", { name: /Attempt 2/ }));

      expect(
        await screen.findByRole("heading", { name: "Attempt 2" }),
      ).toBeInTheDocument();
      expect(
        within(nav).getByRole("link", { name: /Attempt 2/ }),
      ).toHaveAttribute("aria-current", "page");
      expect(
        screen.getByText("Told attempt two to use a list."),
      ).toBeInTheDocument();
      expect(screen.getByText(/revision 3/)).toBeInTheDocument();
      expect(
        screen.getByText(/Runs beside the promoted attempt/),
      ).toBeInTheDocument();
    });

    it("opens straight on the attempt the address names", async () => {
      load(TWO_ATTEMPTS);
      await open(2);

      expect(
        await screen.findByRole("heading", { name: "Attempt 2" }),
      ).toBeInTheDocument();
    });

    it("keeps the spec conversation on the ticket's own view and each attempt's on its tab", async () => {
      load(TWO_ATTEMPTS);
      await open();

      expect(
        screen.getByText("The spec, before work started."),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("Told attempt one to use a map."),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByText("Told attempt two to use a list."),
      ).not.toBeInTheDocument();
    });

    it("reaches a finished attempt too", async () => {
      load({
        id: 5,
        status: "done",
        attempts: [
          makeAttempt({ number: 1, status: "done" }),
          makeAttempt({ number: 2, status: "closed", promoted: false }),
        ],
      });
      await open(2);

      expect(
        await screen.findByRole("heading", { name: "Attempt 2" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("link", { name: /Attempt 1/ }),
      ).toBeInTheDocument();
    });

    it("says so when the address names an attempt the ticket does not have", async () => {
      load(TWO_ATTEMPTS);
      await open(9);

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "no attempt 9",
      );
    });

    it("shows a one-attempt ticket as it always was: no tabs, every comment, the view tabs", async () => {
      load({ id: 5, attempts: [makeAttempt()] });
      await open(1);

      expect(
        screen.queryByRole("navigation", { name: "Attempts" }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("heading", { name: "Attempt 1" }),
      ).not.toBeInTheDocument();
      expect(
        screen.getByRole("tablist", { name: "Ticket views" }),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Told attempt one to use a map."),
      ).toBeInTheDocument();
    });

    it("shows a ticket with no attempts as it always was", async () => {
      load({ id: 5 });
      await open();

      expect(
        screen.queryByRole("navigation", { name: "Attempts" }),
      ).not.toBeInTheDocument();
    });

    it("says an unpromoted finished attempt awaits a choice", async () => {
      load({
        id: 5,
        attempts: [
          makeAttempt({
            number: 1,
            status: "review",
            promoted: false,
            promoted_at: null,
          }),
          makeAttempt({
            number: 2,
            status: "in_progress",
            promoted: false,
            promoted_at: null,
          }),
        ],
      });
      await open(1);

      expect(await screen.findByText("awaiting choice")).toBeInTheDocument();
    });
  });

  describe("blind answering", () => {
    it("leaves the siblings' conversations out of the page until the control is used", async () => {
      load(TWO_ATTEMPTS, [
        ...COMMENTS,
        makeComment({
          id: 4,
          attempt_number: 1,
          body: "A second thing for one.",
        }),
      ]);
      const user = userEvent.setup();
      await open(2);
      await screen.findByRole("heading", { name: "Attempt 2" });

      expect(
        screen.getByText("Told attempt two to use a list."),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("Told attempt one to use a map."),
      ).not.toBeInTheDocument();
      const show = screen.getByRole("button", {
        name: "Show other attempts' conversations",
      });
      expect(show).toHaveAttribute("aria-expanded", "false");

      await user.click(show);

      expect(
        screen.getByText("Told attempt one to use a map."),
      ).toBeInTheDocument();
      expect(screen.getByText("A second thing for one.")).toBeInTheDocument();
      const hide = screen.getByRole("button", {
        name: "Hide other attempts' conversations",
      });
      expect(hide).toHaveAttribute("aria-expanded", "true");

      await user.click(hide);
      expect(
        screen.queryByText("Told attempt one to use a map."),
      ).not.toBeInTheDocument();
    });

    it("starts blind again on another attempt", async () => {
      load(TWO_ATTEMPTS);
      const user = userEvent.setup();
      await open(2);
      await user.click(
        await screen.findByRole("button", {
          name: "Show other attempts' conversations",
        }),
      );
      expect(
        screen.getByText("Told attempt one to use a map."),
      ).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: /Attempt 1/ }));

      await screen.findByRole("heading", { name: "Attempt 1" });
      expect(
        screen.queryByText("Told attempt two to use a list."),
      ).not.toBeInTheDocument();
    });

    it("holds the siblings' handoffs back too", async () => {
      load(TWO_ATTEMPTS);
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([
        makeAgentContext({
          id: 1,
          attempt_number: 1,
          body: "Attempt one's handoff.",
        }),
        makeAgentContext({
          id: 2,
          attempt_number: 2,
          body: "Attempt two's handoff.",
        }),
      ]);
      await open(2);

      expect(
        await screen.findAllByText("Attempt two's handoff."),
      ).not.toHaveLength(0);
      expect(
        screen.queryByText("Attempt one's handoff."),
      ).not.toBeInTheDocument();
    });
  });

  describe("answering and deciding on a tab", () => {
    it("sends the attempt with a comment posted from its tab", async () => {
      load(TWO_ATTEMPTS);
      const add = vi
        .spyOn(apiClient, "addComment")
        .mockResolvedValue(
          makeComment({ id: 9, attempt_number: 2, body: "use a heap" }),
        );
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
      const user = userEvent.setup();
      await open(2);

      await user.type(
        await screen.findByLabelText("Add a comment"),
        "use a heap",
      );
      await user.click(screen.getByRole("button", { name: "Answer attempt 2" }));

      await waitFor(() => expect(add).toHaveBeenCalledWith(5, "use a heap", 2));
      expect(await screen.findByText("use a heap")).toBeInTheDocument();
    });

    it("sends the attempt with a comment that sets needs discussion", async () => {
      load(TWO_ATTEMPTS);
      const add = vi
        .spyOn(apiClient, "addCommentSettingNeedsDiscussion")
        .mockResolvedValue({
          comment: makeComment({ id: 9, attempt_number: 2 }),
          ticket: makeTicket(TWO_ATTEMPTS),
        });
      const user = userEvent.setup();
      await open(2);

      await user.type(await screen.findByLabelText("Add a comment"), "talk");
      await user.click(
        screen.getByRole("button", { name: "Comment on attempt 2 & needs discussion" }),
      );

      await waitFor(() => expect(add).toHaveBeenCalledWith(5, "talk", true, 2));
    });

    it("names no attempt for a comment posted on the ticket's own view", async () => {
      load(TWO_ATTEMPTS);
      const add = vi
        .spyOn(apiClient, "addComment")
        .mockResolvedValue(makeComment({ id: 9, body: "to all" }));
      const user = userEvent.setup();
      await open();

      await user.type(await screen.findByLabelText("Add a comment"), "to all");
      await user.click(
        screen.getByRole("button", { name: "Comment on the ticket (reaches every live attempt)" }),
      );

      await waitFor(() =>
        expect(add).toHaveBeenCalledWith(5, "to all", undefined),
      );
    });

    it("does not carry a draft from one attempt's tab to the next", async () => {
      load(TWO_ATTEMPTS);
      const user = userEvent.setup();
      await open(1);
      await user.type(
        await screen.findByLabelText("Add a comment"),
        "meant for one",
      );

      await user.click(screen.getByRole("link", { name: /Attempt 2/ }));

      await screen.findByRole("heading", { name: "Attempt 2" });
      expect(screen.getByLabelText("Add a comment")).toHaveValue("");
    });

    it("approves only the attempt in review whose tab it is on, and shows the reply", async () => {
      const waiting = {
        ...TWO_ATTEMPTS,
        status: "review" as const,
        attempts: [
          makeAttempt({
            number: 1,
            status: "review",
            pr_url: "https://example.test/pull/1",
          }),
          makeAttempt({
            number: 2,
            status: "review",
            promoted: false,
            pr_url: "https://example.test/pull/2",
          }),
        ],
      };
      load(waiting);
      const approve = vi.spyOn(apiClient, "approveTicket").mockResolvedValue(
        makeTicket({
          ...waiting,
          status: "in_progress",
          attempts: [
            waiting.attempts[0],
            {
              ...waiting.attempts[1],
              status: "queued",
              approved_at: "2026-01-02T00:00:00Z",
            },
          ],
        }),
      );
      const user = userEvent.setup();
      await open(2);

      await user.click(await screen.findByRole("button", { name: "Approve attempt 2" }));

      await waitFor(() => expect(approve).toHaveBeenCalledWith(5, 2));
      // The reply is the attempt now: it is no longer waiting on a review.
      await waitFor(() =>
        expect(
          screen.queryByRole("button", { name: "Approve attempt 2" }),
        ).not.toBeInTheDocument(),
      );
      expect(screen.getAllByText("queued").length).toBeGreaterThan(0);
    });

    it("asks for changes of the attempt whose tab it is on", async () => {
      const waiting = {
        id: 5,
        status: "review" as const,
        attempts: [
          makeAttempt({ number: 1, status: "review" }),
          makeAttempt({ number: 2, status: "review", promoted: false }),
        ],
      };
      load(waiting);
      const ask = vi
        .spyOn(apiClient, "requestTicketChanges")
        .mockResolvedValue(makeTicket(waiting));
      const user = userEvent.setup();
      await open(2);

      await user.type(
        await screen.findByLabelText("What needs to change"),
        "tests",
      );
      await user.click(screen.getByRole("button", { name: "Request changes on attempt 2" }));

      await waitFor(() => expect(ask).toHaveBeenCalledWith(5, "tests", 2));
    });

    it("offers no approval on the ticket's own view, where it would have to guess", async () => {
      load({
        id: 5,
        status: "review",
        attempts: [
          makeAttempt({ number: 1, status: "review" }),
          makeAttempt({ number: 2, status: "review", promoted: false }),
        ],
      });
      await open();

      expect(
        screen.queryByRole("button", { name: "Approve" }),
      ).not.toBeInTheDocument();
    });
  });

  describe("the variant picker", () => {
    it("routes the attempt of its tab through the per-attempt endpoint", async () => {
      load(TWO_ATTEMPTS);
      vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([
        FAST_V1,
        FAST_V2,
      ]);
      const set = vi
        .spyOn(apiClient, "setAttemptVariant")
        .mockResolvedValue(makeTicket(TWO_ATTEMPTS));
      const user = userEvent.setup();
      await open(2);

      await chooseFromDropdown(
        user,
        /Only agents on this variant version/,
        "fast v2",
      );

      await waitFor(() => expect(set).toHaveBeenCalledWith(5, 2, "v2"));
    });

    it("is read-only once the attempt has had its first lease", async () => {
      load({
        ...TWO_ATTEMPTS,
        attempts: [
          makeAttempt({ number: 1 }),
          makeAttempt({
            number: 2,
            promoted: false,
            variant_settable: false,
            variant: {
              version_id: "v2",
              name: "fast",
              version: 2,
              archived: false,
            },
          }),
        ],
      });
      vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
      await open(2);

      expect(
        await screen.findByText(/Only agents on variant fast v2 claim this/),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("combobox", {
          name: /Only agents on this variant version/,
        }),
      ).not.toBeInTheDocument();
    });

    it("is not on the ticket's own view, where there are several to route", async () => {
      load(TWO_ATTEMPTS);
      vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
      await open();

      expect(
        screen.queryByRole("heading", { name: "Variant" }),
      ).not.toBeInTheDocument();
    });
  });

  describe("the Add-attempt form", () => {
    // Not live, so the whole cap is free unless a test says otherwise.
    const FINISHED = makeAttempt({ status: "done" });

    async function openForm(attempts = [FINISHED]) {
      load({ id: 5, attempts });
      vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([
        FAST_V1,
        FAST_V2,
      ]);
      await open();
      return await screen.findByRole("heading", { name: "Add attempts" });
    }

    it("starts with one row and adds three attempts in one request", async () => {
      const add = vi.spyOn(apiClient, "addTicketAttempts").mockResolvedValue(
        makeTicket({
          id: 5,
          attempts: [
            makeAttempt(),
            makeAttempt({ number: 2, promoted: false }),
            makeAttempt({ number: 3, promoted: false }),
            makeAttempt({ number: 4, promoted: false }),
          ],
        }),
      );
      const user = userEvent.setup();
      await openForm();
      expect(
        screen.getAllByRole("combobox", { name: /Variant for new attempt/ }),
      ).toHaveLength(1);

      await chooseFromDropdown(user, "Variant for new attempt 1", "fast v1");
      await user.click(screen.getByRole("button", { name: "Add another" }));
      await user.click(screen.getByRole("button", { name: "Add another" }));
      await chooseFromDropdown(user, "Variant for new attempt 3", "fast v2");
      await user.click(screen.getByRole("button", { name: "Add 3 attempts" }));
      await user.click(
        screen.getByRole("button", { name: "Confirm: add 3 attempts" }),
      );

      await waitFor(() => expect(add).toHaveBeenCalledTimes(1));
      expect(add).toHaveBeenCalledWith(5, ["v1", null, "v2"]);
      // The ticket now has four attempts, so the page is split into tabs.
      expect(
        await screen.findByRole("navigation", { name: "Attempts" }),
      ).toBeInTheDocument();
      expect(
        screen.getAllByRole("combobox", { name: /Variant for new attempt/ }),
      ).toHaveLength(1);
    });

    it("shows the server's refusal as it came back, and adds nothing", async () => {
      vi.spyOn(apiClient, "addTicketAttempts").mockRejectedValue(
        new ApiError(
          409,
          "adding 5 attempts would pass the cap of 5 live attempts",
        ),
      );
      const user = userEvent.setup();
      await openForm();
      for (let i = 0; i < 4; i++)
        await user.click(screen.getByRole("button", { name: "Add another" }));

      await user.click(screen.getByRole("button", { name: "Add 5 attempts" }));
      await user.click(
        screen.getByRole("button", { name: "Confirm: add 5 attempts" }),
      );

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "would pass the cap of 5 live attempts",
      );
      expect(
        screen.queryByRole("navigation", { name: "Attempts" }),
      ).not.toBeInTheDocument();
      expect(
        screen.getAllByRole("combobox", { name: /Variant for new attempt/ }),
      ).toHaveLength(5);
    });

    it("stops at the most the server takes in a batch", async () => {
      const user = userEvent.setup();
      await openForm();
      for (let i = 0; i < 4; i++)
        await user.click(screen.getByRole("button", { name: "Add another" }));

      expect(
        screen.getByRole("button", { name: "Add another" }),
      ).toBeDisabled();
    });

    it("removes a row, but never the last", async () => {
      const user = userEvent.setup();
      await openForm();
      expect(
        screen.queryByRole("button", { name: /Remove row/ }),
      ).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Add another" }));

      await user.click(screen.getByRole("button", { name: "Remove row 2" }));

      expect(
        screen.getAllByRole("combobox", { name: /Variant for new attempt/ }),
      ).toHaveLength(1);
    });

    it("is hidden when the server says the feature is off (404 feature_disabled)", async () => {
      vi.spyOn(apiClient, "addTicketAttempts").mockRejectedValue(
        new ApiError(404, "not found", "feature_disabled"),
      );
      const user = userEvent.setup();
      await openForm();

      await user.click(screen.getByRole("button", { name: "Add attempt" }));
      await user.click(
        screen.getByRole("button", { name: "Confirm: add 1 attempt" }),
      );

      await waitFor(() =>
        expect(
          screen.queryByRole("heading", { name: "Add attempts" }),
        ).not.toBeInTheDocument(),
      );
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("shows an error and keeps the form on a plain 404", async () => {
      vi.spyOn(apiClient, "addTicketAttempts").mockRejectedValue(new ApiError(404, "ticket gone"));
      const user = userEvent.setup();
      await openForm();

      await user.click(screen.getByRole("button", { name: "Add attempt" }));
      await user.click(screen.getByRole("button", { name: "Confirm: add 1 attempt" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("ticket gone");
      expect(screen.getByRole("heading", { name: "Add attempts" })).toBeInTheDocument();
    });

    describe("counts and confirmation", () => {
      it("says how many attempts are live and how many more fit", async () => {
        await openForm([
          makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
          makeAttempt({ number: 2, status: "discussion", promoted: false }),
          makeAttempt({ number: 3, status: "done", promoted: false }),
        ]);

        expect(
          screen.getByText(/2 of 5 live attempts; 3 more can be added/),
        ).toBeInTheDocument();
      });

      it("stops adding rows at the room left under the cap", async () => {
        const user = userEvent.setup();
        await openForm([
          makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
          makeAttempt({ number: 2, status: "queued", promoted: false }),
          makeAttempt({ number: 3, status: "queued", promoted: false }),
        ]);

        await user.click(screen.getByRole("button", { name: "Add another" }));

        expect(
          screen.getByRole("button", { name: "Add another" }),
        ).toBeDisabled();
        expect(
          screen.getAllByRole("combobox", { name: /Variant for new attempt/ }),
        ).toHaveLength(2);
      });

      it("offers nothing at the cap", async () => {
        await openForm(
          [1, 2, 3, 4, 5].map((n) =>
            makeAttempt({ number: n, status: "queued", promoted: n === 1 }),
          ),
        );

        expect(screen.getByText(/no more can be added/)).toBeInTheDocument();
        expect(
          screen.getByRole("button", { name: "Add attempt" }),
        ).toBeDisabled();
      });

      it("sends nothing until confirmed, and says the new ones are shadows once the promoted attempt has started", async () => {
        const add = vi.spyOn(apiClient, "addTicketAttempts");
        const user = userEvent.setup();
        await openForm([
          makeAttempt({ number: 1, status: "in_progress", started_revision: 2 }),
        ]);

        await user.click(screen.getByRole("button", { name: "Add attempt" }));

        const group = screen.getByRole("group", {
          name: "Confirm new attempts",
        });
        expect(group).toHaveTextContent("Add 1 attempt?");
        expect(group).toHaveTextContent(
          "Attempt 1 has started work, so the new attempts are shadows",
        );
        expect(add).not.toHaveBeenCalled();

        await user.click(screen.getByRole("button", { name: "Cancel" }));
        expect(
          screen.queryByRole("group", { name: "Confirm new attempts" }),
        ).not.toBeInTheDocument();
        expect(add).not.toHaveBeenCalled();
      });

      it("warns that an unstarted promoted attempt loses its promotion", async () => {
        const user = userEvent.setup();
        await openForm([makeAttempt({ number: 1, status: "queued" })]);

        await user.click(screen.getByRole("button", { name: "Add attempt" }));

        expect(
          screen.getByRole("group", { name: "Confirm new attempts" }),
        ).toHaveTextContent(
          "Attempt 1 has not started work, so it loses its promotion",
        );
      });

      it("sends one request when the confirm button is hit twice in the same instant", async () => {
        const pending = deferred<Ticket>();
        const add = vi
          .spyOn(apiClient, "addTicketAttempts")
          .mockReturnValue(pending.promise);
        const user = userEvent.setup();
        await openForm();
        await user.click(screen.getByRole("button", { name: "Add attempt" }));
        const confirm = screen.getByRole("button", {
          name: "Confirm: add 1 attempt",
        });

        // One act, so no render lands between the clicks: only a guard that
        // is not React state can stop the second.
        act(() => {
          fireEvent.click(confirm);
          fireEvent.click(confirm);
        });

        expect(add).toHaveBeenCalledTimes(1);
        await act(async () => {
          pending.resolve(
            makeTicket({
              id: 5,
              attempts: [FINISHED, makeAttempt({ number: 2 })],
            }),
          );
        });
      });

      it("keeps the form disabled while the request is pending", async () => {
        const pending = deferred<Ticket>();
        vi.spyOn(apiClient, "addTicketAttempts").mockReturnValue(
          pending.promise,
        );
        const user = userEvent.setup();
        await openForm();
        await user.click(screen.getByRole("button", { name: "Add attempt" }));
        await user.click(
          screen.getByRole("button", { name: "Confirm: add 1 attempt" }),
        );

        expect(
          await screen.findByRole("button", { name: "Adding…" }),
        ).toBeDisabled();
        await act(async () => {
          pending.reject(new ApiError(409, "nope"));
        });
        expect(await screen.findByRole("alert")).toHaveTextContent("nope");
        expect(screen.getByRole("button", { name: "Add attempt" })).toBeEnabled();
      });
    });
  });

  describe("closing an attempt", () => {
    const TICKET: Partial<Ticket> = {
      id: 5,
      status: "in_progress",
      attempts: [
        makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
        makeAttempt({ number: 2, status: "queued", promoted: false }),
        makeAttempt({ number: 3, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
      ],
    };

    it("offers nothing on the promoted attempt", async () => {
      load(TICKET);
      await open(1);
      await screen.findByRole("heading", { name: "Attempt 1" });
      expect(screen.queryByRole("button", { name: /Close attempt/ })).not.toBeInTheDocument();
    });

    it.each(["done", "closed"] as const)("offers nothing on a %s attempt", async (status) => {
      load({ ...TICKET, attempts: [TICKET.attempts![0], makeAttempt({ number: 2, status, promoted: false })] });
      await open(2);
      await screen.findByRole("heading", { name: "Attempt 2" });
      expect(screen.queryByRole("button", { name: /Close attempt/ })).not.toBeInTheDocument();
    });

    describe("an unpromoted attempt left in review", () => {
      const inReview = (ticketStatus: Ticket["status"], promotedStatus: "done" | "in_progress"): Partial<Ticket> => ({
        id: 5,
        status: ticketStatus,
        attempts: [
          makeAttempt({ number: 1, status: promotedStatus }),
          makeAttempt({ number: 2, status: "review", promoted: false, shadow: true }),
        ],
      });

      it("says it was not chosen and offers Close attempt, once the ticket is done", async () => {
        load(inReview("done", "done"));
        await open(2);
        expect(await screen.findByText(/Not chosen: #5 was done through attempt 1/)).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Close attempt 2" })).toBeInTheDocument();
      });

      it("says nothing while the ticket is still in progress", async () => {
        load(inReview("in_progress", "in_progress"));
        await open(2);
        await screen.findByRole("heading", { name: "Attempt 2" });
        expect(screen.queryByText(/Not chosen/)).not.toBeInTheDocument();
      });
    });

    it("sends nothing until confirmed, and says a queued attempt just will not start", async () => {
      load(TICKET);
      const close = vi.spyOn(apiClient, "closeTicketAttempt");
      const user = userEvent.setup();
      await open(2);

      await user.click(await screen.findByRole("button", { name: "Close attempt 2" }));

      const group = screen.getByRole("group", { name: "Confirm closing attempt 2" });
      expect(group).toHaveTextContent("It will not be started.");
      expect(group).not.toHaveTextContent("PR closed");
      expect(close).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      expect(screen.getByRole("button", { name: "Close attempt 2" })).toBeInTheDocument();
    });

    it("says a running attempt's work is stopped and its PR closed, then shows it closed", async () => {
      load(TICKET);
      const close = vi.spyOn(apiClient, "closeTicketAttempt").mockResolvedValue(
        makeTicket({
          ...TICKET,
          attempts: [
            TICKET.attempts![0],
            TICKET.attempts![1],
            makeAttempt({ number: 3, status: "closed", promoted: false }),
          ],
        }),
      );
      const user = userEvent.setup();
      await open(3);

      await user.click(await screen.findByRole("button", { name: "Close attempt 3" }));
      expect(screen.getByRole("group", { name: "Confirm closing attempt 3" })).toHaveTextContent(
        "Its work will be stopped and its PR closed.",
      );
      await user.click(screen.getByRole("button", { name: "Confirm: close attempt 3" }));

      expect(close).toHaveBeenCalledWith(5, 3);
      expect(await screen.findByText(/This attempt was closed/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /Close attempt/ })).not.toBeInTheDocument();
      expect(screen.getByRole("link", { name: /Attempt 3/ })).toHaveClass("is-closed");
    });

    it("stays disabled while the request is pending and shows a refusal", async () => {
      load(TICKET);
      const pending = deferred<Ticket>();
      vi.spyOn(apiClient, "closeTicketAttempt").mockReturnValue(pending.promise);
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Close attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: close attempt 2" }));

      expect(await screen.findByRole("button", { name: "Closing…" })).toBeDisabled();
      await act(async () => {
        pending.reject(new ApiError(409, "already closed"));
      });
      expect(await screen.findByRole("alert")).toHaveTextContent("already closed");
      expect(screen.getByRole("button", { name: "Close attempt 2" })).toBeEnabled();
    });

    it("is hidden when the server says the feature is off (404 feature_disabled)", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "closeTicketAttempt").mockRejectedValue(new ApiError(404, "not found", "feature_disabled"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Close attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: close attempt 2" }));

      await waitFor(() => expect(screen.queryByRole("button", { name: /Close attempt/ })).not.toBeInTheDocument());
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("shows an error and keeps the button on a plain 404", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "closeTicketAttempt").mockRejectedValue(new ApiError(404, "attempt gone"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Close attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: close attempt 2" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("attempt gone");
      expect(screen.getByRole("button", { name: "Close attempt 2" })).toBeInTheDocument();
    });
  });
  describe("promoting an attempt", () => {
    const TICKET: Partial<Ticket> = {
      id: 5,
      status: "in_progress",
      attempts: [
        makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
        makeAttempt({ number: 2, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
        makeAttempt({ number: 3, status: "review", promoted: false, promoted_at: null }),
      ],
    };

    it("offers nothing on the promoted attempt, a finished one, or a done ticket", async () => {
      load(TICKET);
      await open(1);
      await screen.findByRole("heading", { name: "Attempt 1" });
      expect(screen.queryByRole("button", { name: /Promote attempt/ })).not.toBeInTheDocument();
    });

    it("offers nothing on a done ticket", async () => {
      load({ ...TICKET, status: "done" });
      await open(2);
      await screen.findByRole("heading", { name: "Attempt 2" });
      expect(screen.queryByRole("button", { name: /Promote attempt/ })).not.toBeInTheDocument();
    });

    it("sends nothing until confirmed, and names the demotion of the promoted attempt", async () => {
      load(TICKET);
      const promote = vi.spyOn(apiClient, "promoteTicketAttempt");
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      const group = screen.getByRole("group", { name: "Confirm promoting attempt 2" });
      expect(group).toHaveTextContent("retargeted to main");
      expect(group).toHaveTextContent("[DO NOT MERGE]");
      expect(group).toHaveTextContent("Attempt 1 is promoted now: it is demoted");
      expect(group).not.toHaveTextContent("goes back to its agent");
      expect(promote).not.toHaveBeenCalled();
    });

    it("says an attempt awaiting choice goes back to its agent to merge", async () => {
      load(TICKET);
      const user = userEvent.setup();
      await open(3);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 3" }));
      expect(screen.getByRole("group", { name: "Confirm promoting attempt 3" })).toHaveTextContent(
        "goes back to its agent to merge",
      );
    });

    it("sends one request for a second click while pending, then shows the result", async () => {
      load(TICKET);
      const pending = deferred<Ticket>();
      const promote = vi.spyOn(apiClient, "promoteTicketAttempt").mockReturnValue(pending.promise);
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      const confirm = screen.getByRole("button", { name: "Confirm: promote attempt 2" });
      await user.dblClick(confirm);
      expect(promote).toHaveBeenCalledTimes(1);
      expect(promote).toHaveBeenCalledWith(5, 2);
      expect(await screen.findByRole("button", { name: "Promoting…" })).toBeDisabled();
      await act(async () => {
        pending.resolve(
          makeTicket({
            ...TICKET,
            attempts: [
              makeAttempt({ number: 1, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
              makeAttempt({ number: 2, status: "in_progress", started_revision: 1 }),
            ],
          }),
        );
      });
      await waitFor(() => expect(screen.queryByRole("button", { name: /Promote attempt/ })).not.toBeInTheDocument());
    });

    it("takes the ticket's new status from the response", async () => {
      load({ ...TICKET, status: "review" });
      vi.spyOn(apiClient, "promoteTicketAttempt").mockResolvedValue(
        makeTicket({ ...TICKET, status: "in_progress" }),
      );
      const user = userEvent.setup();
      await open(3);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 3" }));
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 3" }));
      await waitFor(() => expect(screen.getByRole("button", { name: "Promote attempt 3" })).toBeEnabled());
      await user.click(screen.getByRole("link", { name: "Ticket" }));
      expect(await screen.findByRole("combobox", { name: "Status" })).toHaveTextContent(/in progress/i);
    });

    it("shows a refusal and re-enables", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "promoteTicketAttempt").mockRejectedValue(new ApiError(409, "scope held"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 2" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("scope held");
    });

    it("shows success when the reply is lost but the re-read shows the attempt promoted", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "promoteTicketAttempt").mockRejectedValue(new ApiError(502, "bad gateway"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          ...TICKET,
          attempts: [
            makeAttempt({ number: 1, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
            makeAttempt({ number: 2, status: "in_progress", promoted: true, started_revision: 1 }),
          ],
        }),
      );
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 2" }));
      await waitFor(() => expect(screen.queryByRole("button", { name: "Promote attempt 2" })).not.toBeInTheDocument());
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("shows the error when the reply is lost and the re-read shows nothing promoted", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "promoteTicketAttempt").mockRejectedValue(new ApiError(502, "bad gateway"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 2" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("bad gateway");
    });

    it("is hidden when the server says the feature is off (404 feature_disabled)", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "promoteTicketAttempt").mockRejectedValue(new ApiError(404, "not found", "feature_disabled"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 2" }));
      await waitFor(() => expect(screen.queryByRole("button", { name: /Promote attempt/ })).not.toBeInTheDocument());
    });

    it("shows an error and keeps the button on a plain 404", async () => {
      load(TICKET);
      vi.spyOn(apiClient, "promoteTicketAttempt").mockRejectedValue(new ApiError(404, "attempt gone"));
      const user = userEvent.setup();
      await open(2);
      await user.click(await screen.findByRole("button", { name: "Promote attempt 2" }));
      await user.click(screen.getByRole("button", { name: "Confirm: promote attempt 2" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("attempt gone");
      expect(screen.getByRole("button", { name: "Promote attempt 2" })).toBeInTheDocument();
    });
  });
  it("shows each attempt's own signals on its tab, unknown when it has none", async () => {
    load({
      ...TWO_ATTEMPTS,
      attempts: [
        makeAttempt({ number: 1, status: "discussion", signals: null }),
        makeAttempt({
          number: 2,
          status: "discussion",
          promoted: false,
          signals: {
            computed_at: "2026-01-01T00:00:00Z", final: true, head_sha: "abc",
            autonomy: { blocked_handbacks: 0, discussions: 0, scope_extension_denials: 0 },
            cheating: {
              assertions_removed: { files: [], known: true },
              out_of_scope: { files: [], known: true },
              skips_added: { files: ["x_test.go"], known: true },
              tests_deleted: { files: [], known: true },
            },
            checks: { own_test_failures: [], passed: "yes", regressions: [], reruns: 0, unattributed: false },
            own_tests: [],
            size: {
              binary_files: 0, files_touched: 1, generated_lines: 0, generated_rules_applied: true,
              generated_skipped: 0, known: true, lines_added: 1, lines_removed: 0,
            },
            time: { first_claim_at: null, last_run_ended_at: null, runs: 1, turns: 1, wall_clock_seconds: 1 },
          },
        }),
      ],
    });
    await open(2);

    const view = within(await screen.findByRole("region", { name: "Signals" }));
    expect(view.getByText("⚑ x_test.go")).toBeInTheDocument();

    await userEvent.setup().click(
      within(screen.getByRole("navigation", { name: "Attempts" })).getByRole("link", { name: /Attempt 1/ }),
    );
    expect(await screen.findByRole("heading", { name: "Attempt 1" })).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Signals" })).getByText("unknown")).toBeInTheDocument();
  });
});
