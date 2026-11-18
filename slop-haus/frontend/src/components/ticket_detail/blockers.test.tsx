import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { chooseFromDropdown, filterAndChoose } from "@/test/widgets";
import { makeBlockerCandidate, makeBlockerList, makeComment, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults, deferred, load, switchTo, noAlert } from "./ticket_detail_test_support";

describe("Blockers", () => {
  stubTicketDetailDefaults();

  describe("blockers", () => {
    // The picker is the server's list, not every ticket on the board:
    // server/internal/ticket/graph.go's blockerCandidates has already
    // dropped this ticket, anything waiting on it, anything that already
    // blocks it and anything finished. Re-deriving any of that here would
    // be a second definition of the cycle rule that could drift from the
    // one the API enforces.
    it("offers exactly the blockers the server says are available", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([
        makeBlockerCandidate({ id: 3, title: "Design the schema" }),
        makeBlockerCandidate({ id: 7, title: "Stand up the API" }),
      ]);

      render(<TicketDetail ticketId={19} />);

      await user.click(await screen.findByRole("combobox", { name: "Add blocker" }));
      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
        "#3 Design the schemaopen",
        "#7 Stand up the APIopen",
      ]);
      expect(apiClient.getBlockerCandidates).toHaveBeenCalledWith(19);
    });

    it("adds a blocker chosen from the list", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([
        makeBlockerCandidate({ id: 18, title: "Build the API", status: "open" }),
      ]);
      vi.spyOn(apiClient, "addBlocker").mockResolvedValue(makeBlockerList({ id: 18, title: "Build the API", status: "open" }));

      render(<TicketDetail ticketId={19} />);

      await screen.findByText("Nothing is blocking this ticket.");
      await filterAndChoose(user, "Add blocker", "18", /#18/);
      await user.click(screen.getByRole("button", { name: "Add" }));

      expect(apiClient.addBlocker).toHaveBeenCalledWith(19, 18);
      expect(await screen.findByText("#18 Build the API")).toBeInTheDocument();
      expect(screen.queryByText("Nothing is blocking this ticket.")).not.toBeInTheDocument();
    });

    // The server moves an open ticket to blocked when it gains an unfinished
    // blocker, and records the change as the human's comment; the view
    // shows both without a reload.
    it("shows the status and the comment the change leaves behind", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, status: "open", depends_on: [] }));
      vi.spyOn(apiClient, "listComments")
        .mockResolvedValueOnce([])
        .mockResolvedValueOnce([makeComment({ id: 1, body: "Blocked on #18: Build the API" })]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([
        makeBlockerCandidate({ id: 18, title: "Build the API", status: "open" }),
      ]);
      vi.spyOn(apiClient, "addBlocker").mockResolvedValue(makeBlockerList({ id: 18, title: "Build the API", status: "open" }));

      render(<TicketDetail ticketId={19} />);

      await screen.findByText("Nothing is blocking this ticket.");
      expect(screen.getByRole("combobox", { name: "Status" })).toHaveTextContent("open");
      await filterAndChoose(user, "Add blocker", "18", /#18/);
      await user.click(screen.getByRole("button", { name: "Add" }));

      expect(await screen.findByText("Blocked on #18: Build the API")).toBeInTheDocument();
      expect(screen.getByRole("combobox", { name: "Status" })).toHaveTextContent("blocked");
    });

    // Adding an edge changes what may be added next -- the ticket just
    // added drops out, and so does anything that now reaches this ticket
    // through it. A list fetched once would keep offering both.
    it("refetches the candidate list after the graph changes", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      const candidates = vi
        .spyOn(apiClient, "getBlockerCandidates")
        .mockResolvedValue([makeBlockerCandidate({ id: 18 })]);
      vi.spyOn(apiClient, "addBlocker").mockResolvedValue(makeBlockerList({ id: 18, title: "Some other ticket", status: "open" }));

      render(<TicketDetail ticketId={19} />);

      await chooseFromDropdown(user, "Add blocker", /#18/);
      expect(candidates).toHaveBeenCalledTimes(1);

      await user.click(screen.getByRole("button", { name: "Add" }));
      await expect.poll(() => candidates.mock.calls.length).toBe(2);
    });

    // Refetching is half of it: the new list has to be the one offered.
    it("offers the refetched list, not the one fetched before an add", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      const candidates = vi
        .spyOn(apiClient, "getBlockerCandidates")
        .mockResolvedValueOnce([makeBlockerCandidate({ id: 18, title: "Old option" })])
        .mockResolvedValue([makeBlockerCandidate({ id: 20, title: "New option" })]);
      vi.spyOn(apiClient, "addBlocker").mockResolvedValue(makeBlockerList({ id: 18, title: "Old option", status: "open" }));

      render(<TicketDetail ticketId={19} />);

      await chooseFromDropdown(user, "Add blocker", /#18/);
      await user.click(screen.getByRole("button", { name: "Add" }));
      await expect.poll(() => candidates.mock.calls.length).toBe(2);

      await user.click(await screen.findByRole("combobox", { name: "Add blocker" }));
      expect(await screen.findByRole("option", { name: /New option/ })).toBeInTheDocument();
      expect(screen.queryByRole("option", { name: /Old option/ })).not.toBeInTheDocument();
    });

    it("offers the refetched list after a blocker is removed", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 19, depends_on: [18], blockers: [{ id: 18, title: "Build the API", status: "open" }] }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      const candidates = vi
        .spyOn(apiClient, "getBlockerCandidates")
        .mockResolvedValueOnce([makeBlockerCandidate({ id: 3, title: "Old option" })])
        .mockResolvedValue([makeBlockerCandidate({ id: 18, title: "Build the API" })]);
      vi.spyOn(apiClient, "removeBlocker").mockResolvedValue(makeBlockerList());

      render(<TicketDetail ticketId={19} />);

      await user.click(await screen.findByRole("button", { name: "Remove" }));
      await expect.poll(() => candidates.mock.calls.length).toBe(2);

      await user.click(await screen.findByRole("combobox", { name: "Add blocker" }));
      expect(await screen.findByRole("option", { name: /#18 Build the API/ })).toBeInTheDocument();
      expect(screen.queryByRole("option", { name: /Old option/ })).not.toBeInTheDocument();
    });

    it("says the tickets are loading while the list is pending", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockReturnValue(new Promise(() => {}));

      render(<TicketDetail ticketId={19} />);

      expect(await screen.findByText("Loading tickets…")).toBeInTheDocument();
    });

    it("says so when nothing is available to block on", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={19} />);

      expect(
        await screen.findByText("No other ticket can block this one without creating a cycle."),
      ).toBeInTheDocument();
      expect(screen.queryByRole("combobox", { name: "Add blocker" })).not.toBeInTheDocument();
    });

    // "Still loading" and "could not load" both look like an empty list,
    // and saying "nothing can block this" in either case states as fact
    // something the UI does not know.
    it("does not claim nothing can block the ticket while the list is loading", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockReturnValue(new Promise(() => {}));

      render(<TicketDetail ticketId={19} />);

      await screen.findByText("Nothing is blocking this ticket.");
      expect(
        screen.queryByText("No other ticket can block this one without creating a cycle."),
      ).not.toBeInTheDocument();
    });

    it("offers a retry when the candidate list cannot be loaded", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 19, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      const candidates = vi
        .spyOn(apiClient, "getBlockerCandidates")
        .mockRejectedValueOnce(new ApiError(503, "unavailable"))
        .mockResolvedValue([makeBlockerCandidate({ id: 18 })]);

      render(<TicketDetail ticketId={19} />);

      await user.click(await screen.findByRole("button", { name: "Try again" }));

      expect(await screen.findByRole("combobox", { name: "Add blocker" })).toBeInTheDocument();
      expect(candidates).toHaveBeenCalledTimes(2);
    });

    // Two edits in quick succession leave two refreshes in flight. If the
    // first one lands last it puts back a ticket that has just been dealt
    // with, and selecting it earns a 422 the picker exists to prevent.
    it("ignores a superseded candidate response that arrives late", async () => {
      const user = userEvent.setup();
      const blocker18 = { id: 18, title: "Design the schema", status: "open" as const };
      const blocker20 = { id: 20, title: "Build the API", status: "open" as const };
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 19, depends_on: [18, 20], blockers: [blocker18, blocker20] }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "removeBlocker")
        .mockResolvedValueOnce(makeBlockerList(blocker20))
        .mockResolvedValue(makeBlockerList());

      let releaseStale: () => void = () => {};
      const stale = [makeBlockerCandidate({ id: 3, title: "stale", status: "open" })];
      const fresh = [makeBlockerCandidate({ id: 4, title: "fresh", status: "open" })];
      let call = 0;
      vi.spyOn(apiClient, "getBlockerCandidates").mockImplementation(() => {
        call += 1;
        if (call === 2) {
          return new Promise((resolve) => {
            releaseStale = () => resolve(stale);
          });
        }
        return Promise.resolve(call === 1 ? [] : fresh);
      });

      render(<TicketDetail ticketId={19} />);

      // Two removes in a row, so two refreshes are in flight at once.
      await user.click((await screen.findAllByRole("button", { name: "Remove" }))[0]);
      await user.click((await screen.findAllByRole("button", { name: "Remove" }))[0]);

      // The panel only renders its options while open.
      await user.click(await screen.findByRole("combobox", { name: "Add blocker" }));
      expect(await screen.findByRole("option", { name: /fresh/ })).toBeInTheDocument();

      await act(async () => {
        releaseStale();
      });

      expect(screen.queryByRole("option", { name: /stale/ })).not.toBeInTheDocument();
      expect(screen.getByRole("option", { name: /fresh/ })).toBeInTheDocument();
    });

    // A bare number says nothing to someone deciding whether to remove it.
    it("names each blocker by its title", async () => {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({
          id: 19,
          depends_on: [17, 18],
          blockers: [
            { id: 17, title: "Design the schema", status: "open" },
            { id: 18, title: "Build the API", status: "open" },
          ],
        }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

      render(<TicketDetail ticketId={19} />);

      expect(await screen.findByText("#17 Design the schema")).toBeInTheDocument();
      expect(screen.getByText("#18 Build the API")).toBeInTheDocument();
    });

    it("removes a blocker", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(
        makeTicket({ id: 19, depends_on: [18], blockers: [{ id: 18, title: "Build the API", status: "open" }] }),
      );
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      vi.spyOn(apiClient, "removeBlocker").mockResolvedValue(makeBlockerList());

      render(<TicketDetail ticketId={19} />);

      await user.click(await screen.findByRole("button", { name: "Remove" }));

      expect(apiClient.removeBlocker).toHaveBeenCalledWith(19, 18);
      expect(await screen.findByText("Nothing is blocking this ticket.")).toBeInTheDocument();
    });

    // The picker means a refusal should now be unreachable through the UI,
    // which is exactly why the handling stays: the list is a snapshot, and
    // another writer can close a cycle between the fetch and the click. A
    // 422 must read as an explanation, not as a silent no-op.
    it("surfaces a readable message when adding a blocker would create a cycle", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 17, depends_on: [] }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([makeBlockerCandidate({ id: 18 })]);
      vi.spyOn(apiClient, "addBlocker").mockRejectedValue(
        new ApiError(422, "ticket: dependency would create a cycle: #17 already depends on #18"),
      );

      render(<TicketDetail ticketId={17} />);

      await screen.findByText("Nothing is blocking this ticket.");
      await chooseFromDropdown(user, "Add blocker", /#18/);
      await user.click(screen.getByRole("button", { name: "Add" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "ticket: dependency would create a cycle: #17 already depends on #18",
      );
      expect(screen.getByText("Nothing is blocking this ticket.")).toBeInTheDocument();
    });
  });

  // The candidate list belongs to one ticket. A slow response for the
  // ticket just left must not land in the next one's picker, where
  // choosing from it would post a blocker the server has never said was
  // allowed for this ticket. The generation guard in refreshCandidates is
  // what stops it -- this fails with that guard removed.
  it("drops a candidate response belonging to the previous ticket", async () => {
    vi.spyOn(apiClient, "getTicket").mockImplementation(async (id: number) => makeTicket({ id }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);

    let releaseOld: () => void = () => {};
    vi.spyOn(apiClient, "getBlockerCandidates").mockImplementation((ticketId: number) => {
      if (ticketId === 5) {
        return new Promise((resolve) => {
          releaseOld = () => resolve([makeBlockerCandidate({ id: 111, title: "belongs to #5" })]);
        });
      }
      return Promise.resolve([makeBlockerCandidate({ id: 222, title: "belongs to #6" })]);
    });

    const { rerender } = render(<TicketDetail ticketId={5} />);
    rerender(<TicketDetail ticketId={6} />);

    await act(async () => {
      releaseOld();
    });

    await screen.findByRole("combobox", { name: "Add blocker" });
    await userEvent.setup().click(screen.getByRole("combobox", { name: "Add blocker" }));

    expect(screen.queryByRole("option", { name: /belongs to #5/ })).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: /belongs to #6/ })).toBeInTheDocument();
  });

  describe("action state", () => {
    describe("blockers", () => {
      const candidates = () =>
        vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([
          makeBlockerCandidate({ id: 18, title: "Build the API", status: "open" }),
        ]);
      const built = { id: 18, title: "Build the API", status: "open" as const };

      async function chooseBlocker(user: ReturnType<typeof userEvent.setup>) {
        await screen.findByText("Nothing is blocking this ticket.");
        await filterAndChoose(user, "Add blocker", "18", /#18/);
      }

      it("Add is disabled and shows its busy label while in flight, then lists the blocker", async () => {
        const user = userEvent.setup();
        load({});
        candidates();
        const reply = deferred<ReturnType<typeof makeBlockerList>>();
        vi.spyOn(apiClient, "addBlocker").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await chooseBlocker(user);
        await user.click(screen.getByRole("button", { name: "Add" }));
        expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();

        await act(async () => reply.resolve(makeBlockerList(built)));

        expect(await screen.findByText("#18 Build the API")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Add" })).toBeEnabled();
        expect(screen.queryByRole("button", { name: "Saving…" })).not.toBeInTheDocument();
      });

      it("Add shows the server's message, and clears it for a retry", async () => {
        const user = userEvent.setup();
        load({});
        candidates();
        const retry = deferred<ReturnType<typeof makeBlockerList>>();
        vi.spyOn(apiClient, "addBlocker")
          .mockRejectedValueOnce(new ApiError(422, "ticket: would create a cycle"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await chooseBlocker(user);
        await user.click(screen.getByRole("button", { name: "Add" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("ticket: would create a cycle");
        expect(screen.getByRole("button", { name: "Add" })).toBeEnabled();

        // The choice survives the refusal, so the retry needs no new pick.
        await user.click(screen.getByRole("button", { name: "Add" }));
        noAlert();
        expect(apiClient.addBlocker).toHaveBeenLastCalledWith(5, 18);

        await act(async () => retry.resolve(makeBlockerList(built)));
        noAlert();
        expect(await screen.findByText("#18 Build the API")).toBeInTheDocument();
      });

      it("drops the add error and the chosen ticket when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load({});
        candidates();
        const add = vi.spyOn(apiClient, "addBlocker").mockRejectedValue(new ApiError(422, "ticket: would create a cycle"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await chooseBlocker(user);
        await user.click(screen.getByRole("button", { name: "Add" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);
        await screen.findByRole("combobox", { name: "Add blocker" });
        noAlert();

        // Nothing is chosen on #6: submitting says so rather than adding #18.
        add.mockClear();
        await user.click(screen.getByRole("button", { name: "Add" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("Choose a ticket.");
        expect(add).not.toHaveBeenCalled();
      });

      describe("Remove", () => {
        beforeEach(() => {
          load({ depends_on: [18], blockers: [built] });
        });

        it("is disabled while in flight, then enabled again", async () => {
          const user = userEvent.setup();
          const reply = deferred<ReturnType<typeof makeBlockerList>>();
          vi.spyOn(apiClient, "removeBlocker").mockReturnValue(reply.promise);
          render(<TicketDetail ticketId={5} />);

          await user.click(await screen.findByRole("button", { name: "Remove" }));
          expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();

          // A reply that still lists the blocker keeps the button on screen.
          await act(async () => reply.resolve(makeBlockerList(built)));

          expect(screen.getByRole("button", { name: "Remove" })).toBeEnabled();
        });

        it("shows the server's message, and clears it for a retry", async () => {
          const user = userEvent.setup();
          const retry = deferred<ReturnType<typeof makeBlockerList>>();
          vi.spyOn(apiClient, "removeBlocker")
            .mockRejectedValueOnce(new ApiError(404, "ticket: no such blocker"))
            .mockReturnValueOnce(retry.promise);
          render(<TicketDetail ticketId={5} />);

          await user.click(await screen.findByRole("button", { name: "Remove" }));
          expect(await screen.findByRole("alert")).toHaveTextContent("ticket: no such blocker");
          expect(screen.getByRole("button", { name: "Remove" })).toBeEnabled();

          await user.click(screen.getByRole("button", { name: "Remove" }));
          noAlert();

          await act(async () => retry.resolve(makeBlockerList()));
          noAlert();
          expect(await screen.findByText("Nothing is blocking this ticket.")).toBeInTheDocument();
        });
      });
    });
  });
});
