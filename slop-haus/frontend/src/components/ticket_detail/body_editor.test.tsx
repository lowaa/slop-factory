import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeRevision, makeTicket } from "@/test/fixtures";
import { TicketDetail, EDIT, stubTicketDetailDefaults, deferred, load, switchTo, noAlert } from "./ticket_detail_test_support";
import { makeRunningAttempt } from "@/test/running_attempt";

describe("BodyEditor", () => {
  stubTicketDetailDefaults();

  // Rendered without a key, as a caller that forgot one would: the
  // failure it guards against -- one ticket's unsaved body edit sitting
  // over another ticket's detail -- is silent and writes to the wrong
  // ticket if saved.
  it("starts clean when the selected ticket changes", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getTicket").mockImplementation(async (id: number) =>
      makeTicket({ id, body: `body of ${id}` }),
    );
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    const candidates = vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);

    const { rerender } = render(<TicketDetail ticketId={5} />);

    await user.click(await screen.findByRole("button", { name: EDIT }));
    await user.clear(screen.getByLabelText("Description"));
    await user.type(screen.getByLabelText("Description"), "half-written");

    rerender(<TicketDetail ticketId={6} />);

    expect(await screen.findByText("body of 6")).toBeInTheDocument();
    expect(screen.queryByLabelText("Description")).not.toBeInTheDocument();
    expect(candidates).toHaveBeenLastCalledWith(6);
  });

  describe("editing the title and description", () => {
    function renderEditable(ticket = makeTicket({ id: 5, title: "Delete button", body: "first draft", revision: 2 })) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      render(<TicketDetail ticketId={5} />);
    }

    it("saves an edited title and description, against the revision it was opened on", async () => {
      const user = userEvent.setup();
      renderEditable();
      vi.spyOn(apiClient, "editTicket").mockResolvedValue(
        makeTicket({ id: 5, title: "Reinstall script", body: "second draft", revision: 3 }),
      );

      await user.click(await screen.findByRole("button", { name: EDIT }));
      // Editing starts from what is there, not from empty.
      expect(screen.getByLabelText("Title")).toHaveValue("Delete button");
      expect(screen.getByLabelText("Description")).toHaveValue("first draft");

      await user.clear(screen.getByLabelText("Title"));
      await user.type(screen.getByLabelText("Title"), "Reinstall script");
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "second draft");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(apiClient.editTicket).toHaveBeenCalledWith(5, {
        title: "Reinstall script",
        body: "second draft",
        base_revision: 2,
      });
      expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("#5 Reinstall script");
      expect(screen.getByText("second draft")).toBeInTheDocument();
      expect(screen.queryByLabelText("Description")).not.toBeInTheDocument();
    });

    it("sends only the field that changed, and refreshes the history", async () => {
      const user = userEvent.setup();
      renderEditable();
      vi.spyOn(apiClient, "editTicket").mockResolvedValue(
        makeTicket({ id: 5, title: "Reinstall script", body: "first draft", revision: 3 }),
      );

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Title"));
      await user.type(screen.getByLabelText("Title"), "Reinstall script");
      vi.mocked(apiClient.listRevisions).mockResolvedValue([
        makeRevision({ revision: 3, old_title: "Delete button", new_title: "Reinstall script" }),
      ]);
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(apiClient.editTicket).toHaveBeenCalledWith(5, { title: "Reinstall script", base_revision: 2 });
      expect(await screen.findByText("Revision 3")).toBeInTheDocument();
    });

    it("just closes the editor when nothing changed", async () => {
      const user = userEvent.setup();
      renderEditable();
      const edit = vi.spyOn(apiClient, "editTicket");

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(edit).not.toHaveBeenCalled();
      expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
    });

    it("refuses a blank title without asking the server", async () => {
      const user = userEvent.setup();
      renderEditable();
      const edit = vi.spyOn(apiClient, "editTicket");

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Title"));
      await user.type(screen.getByLabelText("Title"), "   ");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("The title can't be empty.");
      expect(edit).not.toHaveBeenCalled();
    });

    // A save that fails must not cost the human the text they just typed.
    it("keeps the editor and the draft when saving fails", async () => {
      const user = userEvent.setup();
      renderEditable();
      vi.spyOn(apiClient, "editTicket").mockRejectedValue(new ApiError(422, "ticket: something was wrong"));

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "worth keeping");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("ticket: something was wrong");
      expect(screen.getByLabelText("Description")).toHaveValue("worth keeping");
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    });

    // An agent (or another tab) edited the ticket after this editor opened.
    // The server refuses rather than overwriting; the human is told why,
    // and keeps their draft.
    it("explains an edit made meanwhile, and keeps the draft", async () => {
      const user = userEvent.setup();
      renderEditable();
      vi.spyOn(apiClient, "editTicket").mockRejectedValue(
        new ApiError(409, "ticket: the ticket was edited since you opened it"),
      );

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "mine");

      vi.mocked(apiClient.getTicket).mockResolvedValue(
        makeTicket({ id: 5, title: "Renamed by the agent", body: "their text", revision: 3 }),
      );
      vi.mocked(apiClient.listRevisions).mockResolvedValue([
        makeRevision({ revision: 3, old_title: "Delete button", new_title: "Renamed by the agent" }),
      ]);
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("Someone else edited this ticket after you started");
      expect(screen.getByLabelText("Description")).toHaveValue("mine");
      // Their change is fetched, so it shows, and editing again starts from it.
      expect(await screen.findByText("Revision 3")).toBeInTheDocument();
      expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Renamed by the agent");
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      await user.click(screen.getByRole("button", { name: EDIT }));
      expect(screen.getByLabelText("Description")).toHaveValue("their text");

      vi.mocked(apiClient.editTicket).mockResolvedValue(makeTicket({ id: 5, body: "mine again", revision: 4 }));
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "mine again");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(apiClient.editTicket).toHaveBeenLastCalledWith(5, { body: "mine again", base_revision: 3 });
    });

    it("discards the draft on cancel", async () => {
      const user = userEvent.setup();
      renderEditable();
      const edit = vi.spyOn(apiClient, "editTicket");

      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "thrown away");
      await user.click(screen.getByRole("button", { name: "Cancel" }));

      expect(edit).not.toHaveBeenCalled();
      expect(screen.getByText("first draft")).toBeInTheDocument();

      // Reopening starts from the saved text again, not from the draft.
      await user.click(screen.getByRole("button", { name: EDIT }));
      expect(screen.getByLabelText("Description")).toHaveValue("first draft");
    });
  });

  describe("action state", () => {
    describe("editing the title and description", () => {
      async function openEditor(user: ReturnType<typeof userEvent.setup>) {
        await user.click(await screen.findByRole("button", { name: EDIT }));
        await user.clear(screen.getByLabelText("Title"));
        await user.type(screen.getByLabelText("Title"), "Renamed");
      }

      it("Save is disabled and shows its busy label while in flight, and is enabled again after a failure", async () => {
        const user = userEvent.setup();
        load({ title: "Original" });
        const reply = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "editTicket").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await openEditor(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
        expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();

        await act(async () => reply.reject(new ApiError(422, "ticket: bad title")));

        expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
        expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
      });

      it("shows the server's message, clears it for a retry, and shows the saved ticket", async () => {
        const user = userEvent.setup();
        load({ title: "Original" });
        const retry = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "editTicket")
          .mockRejectedValueOnce(new ApiError(422, "ticket: bad title"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await openEditor(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("ticket: bad title");

        await user.click(screen.getByRole("button", { name: "Save" }));
        noAlert();

        await act(async () => retry.resolve(makeTicket({ id: 5, title: "Renamed", revision: 1 })));
        noAlert();
        expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("#5 Renamed");
        expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
      });

      it("drops the editor and its error when the selected ticket changes", async () => {
        const user = userEvent.setup();
        load({ title: "Original" });
        vi.spyOn(apiClient, "editTicket").mockRejectedValue(new ApiError(422, "ticket: bad title"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await openEditor(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);

        noAlert();
        expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
        expect(screen.getByRole("button", { name: EDIT })).toBeInTheDocument();
      });

      it("does not merge the previous ticket's late save into the next one", async () => {
        const user = userEvent.setup();
        load({ title: "Original" });
        const saved = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "editTicket").mockReturnValue(saved.promise);
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await openEditor(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        await switchTo(rerender, 6);
        // Closed by the switch itself, not by the old save settling below.
        expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();

        await act(async () => saved.resolve(makeTicket({ id: 5, title: "Renamed", revision: 1 })));

        expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("#6 Original");
        expect(screen.getByRole("button", { name: EDIT })).toBeEnabled();
      });

      it("does not report the previous ticket's late save failure on the next one", async () => {
        const user = userEvent.setup();
        load({ title: "Original" });
        const saved = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "editTicket").mockReturnValue(saved.promise);
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await openEditor(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        await switchTo(rerender, 6);
        // Open on #6, where a failure that still counted would show.
        await user.click(screen.getByRole("button", { name: EDIT }));

        await act(async () => saved.reject(new ApiError(422, "ticket: bad title")));

        noAlert();
        expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
      });
    });
  });

  describe("editing while attempts run", () => {
    const TWO = [makeRunningAttempt(1, 3), makeRunningAttempt(2, 5, { shadow: true })];

    async function openEditor(attempts: ReturnType<typeof makeRunningAttempt>[]) {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, title: "T", body: "old", revision: 5, attempts }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const edit = vi.spyOn(apiClient, "editTicket").mockResolvedValue(makeTicket({ id: 5, title: "T", body: "new", revision: 6 }));
      render(<TicketDetail ticketId={5} />);
      await user.click(await screen.findByRole("button", { name: EDIT }));
      await user.clear(screen.getByLabelText("Description"));
      await user.type(screen.getByLabelText("Description"), "new");
      return { user, edit };
    }

    it("holds the save for a warning naming each attempt's revision, and saves on confirm", async () => {
      const { user, edit } = await openEditor(TWO);
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(screen.getByRole("note")).toHaveTextContent(
        "Attempt 1 started from revision 3, attempt 2 from revision 5. Editing changes the spec under them.",
      );
      expect(edit).not.toHaveBeenCalled();

      await user.click(screen.getByRole("button", { name: "Save anyway" }));
      expect(edit).toHaveBeenCalledWith(5, { body: "new", base_revision: 5 });
    });

    it("withdraws the warning when the draft changes", async () => {
      const { user } = await openEditor(TWO);
      await user.click(screen.getByRole("button", { name: "Save" }));
      await user.type(screen.getByLabelText("Description"), "!");
      expect(screen.queryByRole("note")).not.toBeInTheDocument();
    });

    it("saves straight away with one attempt", async () => {
      const { user, edit } = await openEditor([makeRunningAttempt(1, 3)]);
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(screen.queryByRole("note")).not.toBeInTheDocument();
      expect(edit).toHaveBeenCalledTimes(1);
    });

    it("counts attempts in discussion and review as running", async () => {
      const { user, edit } = await openEditor([
        makeRunningAttempt(1, 3, { status: "discussion" }),
        makeRunningAttempt(2, 5, { status: "review" }),
      ]);
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(screen.getByRole("note")).toBeInTheDocument();
      expect(edit).not.toHaveBeenCalled();
    });

    it("ignores attempts that are not running", async () => {
      const { user, edit } = await openEditor([
        makeRunningAttempt(1, 3),
        makeRunningAttempt(2, null, { status: "queued" }),
        makeRunningAttempt(3, 4, { status: "closed" }),
      ]);
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(edit).toHaveBeenCalledTimes(1);
    });
  });
});
