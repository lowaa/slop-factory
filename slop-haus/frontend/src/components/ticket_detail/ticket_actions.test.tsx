import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import {} from "@/test/render_signed_in";
import {
  chooseFromDropdown,
  filterAndChoose,
  selectedValueOf,
} from "@/test/widgets";
import {  } from "@/components/proposals";
import {
  makeBlockerCandidate,
  makeBlockerList,
  makeTicket,
} from "@/test/fixtures";
import {} from "@/shared/names/agent_name";
import {} from "@/shared/names/member_name";
import {
  TicketDetail,
  describeStatusButtons,
  statusButtonRows,
  stubTicketDetailDefaults,
  } from "./ticket_detail_test_support";
describe("TicketDetail actions", () => {
  stubTicketDetailDefaults();

  describe("preparing", () => {
    it("shows a banner and an Open action for a preparing ticket", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={5} />);

      expect(await screen.findByText("Not in the queue.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Open" })).toBeInTheDocument();
    });

    it("shows no preparing banner or Ready action for a non-preparing ticket", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "open" }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByText("Not in the queue.")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Open" }),
      ).not.toBeInTheDocument();
    });

    // The server's Ready route decides open vs. blocked; this only shows
    // whatever it comes back with, the same way handleAddComment's refresh
    // trusts the server's answer rather than re-deriving it.
    it("clicking Open calls the API and shows the status it comes back with", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "markTicketReady").mockResolvedValue(
        makeTicket({ id: 5, status: "open" }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Open" }));

      expect(apiClient.markTicketReady).toHaveBeenCalledWith(5);
      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByText("Not in the queue.")).not.toBeInTheDocument();
      expect(selectedValueOf("Status")).toContain("open");
    });

    // A dependency the server finds unfinished lands the ticket on
    // "blocked" instead of "open" -- the same rule the answered-discussion
    // path already uses (decision 2 in the plan doc).
    it("shows blocked when opening lands there because of unfinished dependencies", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "markTicketReady").mockResolvedValue(
        makeTicket({ id: 5, status: "blocked" }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Open" }));

      expect(await screen.findByText(/blocked/)).toBeInTheDocument();
      expect(selectedValueOf("Status")).toContain("blocked");
    });

    it("goes inert while Open is in flight, so a second click sends nothing", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      let resolveReady: (
        ticket: ReturnType<typeof makeTicket>,
      ) => void = () => {};
      vi.spyOn(apiClient, "markTicketReady").mockReturnValue(
        new Promise((resolve) => {
          resolveReady = resolve;
        }),
      );

      render(<TicketDetail ticketId={5} />);

      const ready = await screen.findByRole("button", { name: "Open" });
      await user.click(ready);
      expect(screen.getByRole("button", { name: "Opening…" })).toBeDisabled();
      await user.click(screen.getByRole("button", { name: "Opening…" }));
      expect(apiClient.markTicketReady).toHaveBeenCalledTimes(1);

      resolveReady(makeTicket({ id: 5, status: "open" }));
      await waitFor(() => expect(selectedValueOf("Status")).toContain("open"));
    });

    // Ready's reply must not undo a status the human chose after clicking
    // it: the later choice wins, whichever response lands last.
    it("does not let a late Open reply overwrite a status chosen after it", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      let resolveReady: (
        ticket: ReturnType<typeof makeTicket>,
      ) => void = () => {};
      vi.spyOn(apiClient, "markTicketReady").mockReturnValue(
        new Promise((resolve) => {
          resolveReady = resolve;
        }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "cancelled" }),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Open" }));
      await chooseFromDropdown(user, "Status", "cancelled");
      await waitFor(() =>
        expect(selectedValueOf("Status")).toContain("cancelled"),
      );

      await act(async () =>
        resolveReady(makeTicket({ id: 5, status: "open" })),
      );
      expect(selectedValueOf("Status")).toContain("cancelled");
    });

    it("shows an inline alert, and keeps the banner, when opening fails", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "markTicketReady").mockRejectedValue(
        new ApiError(409, "ticket: not preparing"),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Open" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "ticket: not preparing",
      );
      expect(screen.getByText("Not in the queue.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Open" })).toBeEnabled();
    });

    // Whether Ready applies is the server's call, not the status's, both
    // ways round: a status the server says can't be readied gets no
    // button, and one it says can gets one.
    it.each([
      { status: "preparing", can_ready: false },
      { status: "open", can_ready: true },
    ] as const)(
      "follows can_ready rather than the status: $status, can_ready $can_ready",
      async (moves) => {
        vi.spyOn(apiClient, "getTicket").mockResolvedValue(
          makeTicket({ id: 5, ...moves }),
        );
        vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
        vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

        render(<TicketDetail ticketId={5} />);

        await screen.findByRole("heading", { name: /#5/ });
        expect(screen.queryByRole("button", { name: "Open" }) !== null).toBe(
          moves.can_ready,
        );
      },
    );
  });

  // The status control offers what the server says the ticket's status
  // allows, and nothing it has worked out for itself.
  describe("allowed moves", () => {
    function loadTicket(ticket: ReturnType<typeof makeTicket>) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    }

    async function offeredStatuses(user: ReturnType<typeof userEvent.setup>) {
      await user.click(screen.getByRole("combobox", { name: "Status" }));
      const offered = screen.getAllByRole("option").map((o) => o.textContent);
      await user.keyboard("{Escape}");
      return offered;
    }

    // An order and a set no real status has, so only following the
    // server's list exactly passes.
    it("offers exactly the moves the server gave, in its order", async () => {
      const user = userEvent.setup();
      loadTicket(
        makeTicket({
          id: 5,
          status: "open",
          allowed_moves: ["done", "open", "in_progress"],
        }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(await offeredStatuses(user)).toEqual([
        "done",
        "open",
        "in progress",
      ]);
    });

    it("offers the moves of the status a move lands on", async () => {
      const user = userEvent.setup();
      loadTicket(
        makeTicket({ id: 5, status: "open", allowed_moves: ["open", "done"] }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "done",
          allowed_moves: ["done", "cancelled"],
        }),
      );

      render(<TicketDetail ticketId={5} />);

      await chooseFromDropdown(user, "Status", "done");
      await waitFor(() => expect(selectedValueOf("Status")).toContain("done"));
      expect(await offeredStatuses(user)).toEqual(["done", "cancelled"]);
    });

    // Adding a blocker can move open to blocked, so its reply's moves come
    // along with its status.
    it("offers the moves of the status a blocker change lands on", async () => {
      const user = userEvent.setup();
      loadTicket(
        makeTicket({
          id: 19,
          status: "open",
          allowed_moves: ["open", "parked"],
        }),
      );
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([
        makeBlockerCandidate({ id: 18, title: "Build the API" }),
      ]);
      vi.spyOn(apiClient, "addBlocker").mockResolvedValue({
        ...makeBlockerList({ id: 18, title: "Build the API", status: "open" }),
        allowed_moves: ["blocked", "done"],
      });

      render(<TicketDetail ticketId={19} />);

      expect(
        await screen.findByRole("button", { name: "Park" }),
      ).toBeInTheDocument();
      await filterAndChoose(user, "Add blocker", "18", /#18/);
      await user.click(screen.getByRole("button", { name: "Add" }));

      await waitFor(() =>
        expect(selectedValueOf("Status")).toContain("blocked"),
      );
      expect(await offeredStatuses(user)).toEqual(["blocked", "done"]);
      expect(
        screen.queryByRole("button", { name: "Park" }),
      ).not.toBeInTheDocument();
    });
  });

  describe("parked", () => {
    function loadTicket(ticket: ReturnType<typeof makeTicket>) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    }

    it.each(["preparing", "open"] as const)(
      "parks a %s ticket, and shows it parked",
      async (status) => {
        const user = userEvent.setup();
        loadTicket(makeTicket({ id: 5, status }));
        vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
          makeTicket({ id: 5, status: "parked" }),
        );

        render(<TicketDetail ticketId={5} />);

        await user.click(await screen.findByRole("button", { name: "Park" }));

        expect(apiClient.updateTicket).toHaveBeenCalledExactlyOnceWith(5, {
          status: "parked",
        });
        expect(await screen.findByText("Parked.")).toBeInTheDocument();
        expect(selectedValueOf("Status")).toContain("parked");
        expect(
          screen.queryByRole("button", { name: "Park" }),
        ).not.toBeInTheDocument();
      },
    );

    // Park is only offered where the server accepts it.
    it("offers no Park where the server does not allow parking", async () => {
      loadTicket(
        makeTicket({ id: 5, status: "open", allowed_moves: ["open", "done"] }),
      );

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(
        screen.queryByRole("button", { name: "Park" }),
      ).not.toBeInTheDocument();
    });

    it("unparks to preparing, never straight into the queue", async () => {
      const user = userEvent.setup();
      loadTicket(makeTicket({ id: 5, status: "parked" }));
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "preparing", can_ready: true }),
      );

      render(<TicketDetail ticketId={5} />);

      expect(await screen.findByText("Parked.")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Unpark" }));

      expect(apiClient.updateTicket).toHaveBeenCalledExactlyOnceWith(5, {
        status: "preparing",
      });
      expect(await screen.findByText("Not in the queue.")).toBeInTheDocument();
      expect(screen.queryByText("Parked.")).not.toBeInTheDocument();
      expect(selectedValueOf("Status")).toContain("preparing");
    });

    // Parking through the status control drops first claim just as the
    // button does, so the note saying who has it goes too.
    it("drops the first-claim note when an open ticket is parked from the status control", async () => {
      const user = userEvent.setup();
      const inAnHour = new Date(Date.now() + 3_600_000).toISOString();
      loadTicket(
        makeTicket({
          id: 5,
          status: "open",
          resume_agent_id: "agent-42",
          affinity_until: inAnHour,
        }),
      );
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "parked",
          resume_agent_id: null,
          affinity_until: null,
        }),
      );

      render(<TicketDetail ticketId={5} />);

      expect(
        await screen.findByText(/has first claim on this ticket/),
      ).toBeInTheDocument();
      await chooseFromDropdown(user, "Status", "parked");

      expect(await screen.findByText("Parked.")).toBeInTheDocument();
      expect(
        screen.queryByText(/has first claim on this ticket/),
      ).not.toBeInTheDocument();
    });

    it("reports a refusal beside the button and keeps the ticket as it was", async () => {
      const user = userEvent.setup();
      loadTicket(makeTicket({ id: 5, status: "open" }));
      vi.spyOn(apiClient, "updateTicket").mockRejectedValue(
        new ApiError(
          409,
          "ticket: only a preparing or open ticket can be parked",
        ),
      );

      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: "Park" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "only a preparing or open ticket can be parked",
      );
      expect(selectedValueOf("Status")).toContain("open");
    });

    it("says which blocker is parked", async () => {
      loadTicket(
        makeTicket({
          id: 19,
          status: "blocked",
          depends_on: [18, 17],
          blockers: [
            { id: 18, title: "Someday", status: "parked" },
            { id: 17, title: "Underway", status: "in_progress" },
          ],
        }),
      );

      render(<TicketDetail ticketId={19} />);

      expect(await screen.findByText(/#18 Someday/)).toHaveTextContent(
        "#18 Someday, parked",
      );
      expect(screen.getByText(/#17 Underway/)).not.toHaveTextContent("parked");
    });
  });

  describe("action state", () => {
    describeStatusButtons(statusButtonRows("Open", "Park", "Unpark"));
  });

  describe("release cooldown", () => {
    const NOW = new Date("2026-10-10T12:00:00Z");
    const LATER = new Date("2026-10-10T12:30:00Z");

    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(NOW);
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    function loadTicket(ticket: ReturnType<typeof makeTicket>) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    }

    it("shows the re-check time, the streak and a Check now that makes the board's move to open", async () => {
      const user = userEvent.setup();
      loadTicket(makeTicket({ id: 5, status: "blocked", blocked_until: LATER.toISOString(), cooldown_releases: 2 }));
      const update = vi
        .spyOn(apiClient, "updateTicket")
        .mockResolvedValue(makeTicket({ id: 5, status: "open", blocked_until: null }));

      render(<TicketDetail ticketId={5} />);

      const line = await screen.findByText(/re-check after/);
      expect(line).toHaveTextContent("(check 2)");
      expect(line).toHaveAttribute("datetime", LATER.toISOString());
      await user.click(screen.getByRole("button", { name: "Check now" }));

      expect(update).toHaveBeenCalledWith(5, { status: "open" });
      await waitFor(() => expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument());
    });

    it("drops the cooldown when the ticket stays blocked by a dependency after Check now", async () => {
      const user = userEvent.setup();
      loadTicket(makeTicket({ id: 5, status: "blocked", blocked_until: LATER.toISOString(), cooldown_releases: 2 }));
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({ id: 5, status: "blocked", blocked_until: null }),
      );

      render(<TicketDetail ticketId={5} />);
      await user.click(await screen.findByRole("button", { name: "Check now" }));

      await waitFor(() => expect(screen.queryByText(/re-check after/)).not.toBeInTheDocument());
      expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument();
    });

    it.each([
      ["a ticket blocked only by its dependencies", { status: "blocked" as const, blocked_until: null }],
      ["a cooldown that has passed", { status: "blocked" as const, blocked_until: new Date(NOW.getTime() - 60_000).toISOString() }],
      ["an open ticket", { status: "open" as const, blocked_until: LATER.toISOString() }],
    ])("offers no Check now for %s", async (_name, fields) => {
      loadTicket(makeTicket({ id: 5, ...fields }));

      render(<TicketDetail ticketId={5} />);

      await screen.findByRole("heading", { name: /#5/ });
      expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument();
      expect(screen.queryByText(/re-check after/)).not.toBeInTheDocument();
    });
  });
});
