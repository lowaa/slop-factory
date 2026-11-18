import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeAgentRole, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults, deferred, load, switchTo, noAlert } from "./ticket_detail_test_support";

describe("RolesEditor", () => {
  stubTicketDetailDefaults();

  describe("role requirement", () => {
    function renderWithTicket(ticket: ReturnType<typeof makeTicket>) {
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      render(<TicketDetail ticketId={ticket.id} />);
    }

    it("shows no roles section when the tenant has none and the ticket requires none", async () => {
      renderWithTicket(makeTicket({ id: 5, required_roles: [] }));

      expect(await screen.findByRole("heading", { level: 1 })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Roles" })).not.toBeInTheDocument();
    });

    it("says any agent may claim it when the tenant has roles but this ticket requires none", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [] }));

      expect(await screen.findByText("Any agent may claim this.")).toBeInTheDocument();
    });

    it("describes a single required role", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "deploy" })]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));

      expect(await screen.findByText("role: deploy")).toBeInTheDocument();
    });

    it("describes several required roles with their match mode", async () => {
      renderWithTicket(
        makeTicket({
          id: 5,
          required_roles: [
            { id: "r1", name: "deploy" },
            { id: "r2", name: "review" },
          ],
          role_match: "all",
        }),
      );

      expect(await screen.findByText("roles: all of deploy, review")).toBeInTheDocument();
    });

    it("warns when no enabled agent can claim the ticket", async () => {
      renderWithTicket(
        makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], no_eligible_agent: true }),
      );

      expect(await screen.findByRole("status")).toHaveTextContent(
        "No enabled agent has the roles this ticket needs, so no agent can claim it.",
      );
    });

    it("shows no warning when an agent can claim it", async () => {
      renderWithTicket(
        makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], no_eligible_agent: false }),
      );

      await screen.findByText("role: deploy");
      expect(
        screen.queryByText("No enabled agent has the roles this ticket needs, so no agent can claim it."),
      ).not.toBeInTheDocument();
    });

    // A roster fetch that failed must read as "couldn't load", not as "this
    // tenant has no roles" -- and saving unchanged must not resend an empty
    // requirement just because the picker couldn't show it.
    it("shows a load-failure hint in the editor, and saving unchanged keeps the ticket's own roles", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockRejectedValue(new Error("network down"));
      renderWithTicket(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));
      // Echoes back exactly what was sent, the way the real server would --
      // a mock that invented a different result wouldn't tell us whether
      // the UI shows the server's answer or something it made up.
      const setRoles = vi
        .spyOn(apiClient, "setTicketRoles")
        .mockResolvedValue(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      expect(await screen.findByText("Couldn't load your tenant's roles -- try again in a moment.")).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(setRoles).toHaveBeenCalledWith(5, ["r1"], "any");
      expect(await screen.findByText("role: deploy")).toBeInTheDocument();
      expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
    });

    it("opens the editor with the ticket's current roles already checked", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "deploy" }),
        makeAgentRole({ id: "r2", name: "review" }),
      ]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));

      const editor = screen.getByRole("group", { name: "Roles needed to claim this" });
      expect(within(editor).getByRole("checkbox", { name: "deploy" })).toBeChecked();
      expect(within(editor).getByRole("checkbox", { name: "review" })).not.toBeChecked();
    });

    it("saves a changed selection and match mode, and shows the result", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "deploy" }),
        makeAgentRole({ id: "r2", name: "review" }),
      ]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));
      vi.spyOn(apiClient, "setTicketRoles").mockResolvedValue(
        makeTicket({
          id: 5,
          required_roles: [
            { id: "r1", name: "deploy" },
            { id: "r2", name: "review" },
          ],
          role_match: "all",
        }),
      );

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles needed to claim this" });
      await user.click(within(editor).getByRole("checkbox", { name: "review" }));
      await user.click(screen.getByRole("radio", { name: "all of these" }));
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(apiClient.setTicketRoles).toHaveBeenCalledWith(5, ["r1", "r2"], "all");
      expect(await screen.findByText("roles: all of deploy, review")).toBeInTheDocument();
      expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
    });

    it("shows the warning a save's response carries", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "deploy" })]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [], role_match: "any", no_eligible_agent: false }));
      vi.spyOn(apiClient, "setTicketRoles").mockResolvedValue(
        makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any", no_eligible_agent: true }),
      );

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
      await user.click(
        within(screen.getByRole("group", { name: "Roles needed to claim this" })).getByRole("checkbox", { name: "deploy" }),
      );
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("status")).toHaveTextContent(
        "No enabled agent has the roles this ticket needs, so no agent can claim it.",
      );
    });

    it("cancel closes the editor and calls setTicketRoles for nobody", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "deploy" })]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" }));
      const setRoles = vi.spyOn(apiClient, "setTicketRoles");

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles needed to claim this" });
      await user.click(within(editor).getByRole("checkbox", { name: "deploy" }));
      await user.click(screen.getByRole("button", { name: "Cancel" }));

      expect(setRoles).not.toHaveBeenCalled();
      expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
      expect(screen.getByText("role: deploy")).toBeInTheDocument();
    });

    it("keeps the editor open and shows the server's refusal on failure", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "deploy" })]);
      renderWithTicket(makeTicket({ id: 5, required_roles: [], role_match: "any" }));
      vi.spyOn(apiClient, "setTicketRoles").mockRejectedValue(new ApiError(422, "role ids must be valid uuids"));

      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles needed to claim this" });
      await user.click(within(editor).getByRole("checkbox", { name: "deploy" }));
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("role ids must be valid uuids");
      expect(screen.getByRole("group", { name: "Roles needed to claim this" })).toBeInTheDocument();
      expect(within(screen.getByRole("group", { name: "Roles needed to claim this" })).getByRole("checkbox", { name: "deploy" })).toBeChecked();
    });
  });

  describe("action state", () => {
    describe("roles editor", () => {
      beforeEach(() => {
        vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "deploy" })]);
        load({ required_roles: [], role_match: "any" });
      });

      async function pickDeploy(user: ReturnType<typeof userEvent.setup>) {
        await user.click(await screen.findByRole("button", { name: "Edit roles" }));
        await user.click(
          within(screen.getByRole("group", { name: "Roles needed to claim this" })).getByRole("checkbox", {
            name: "deploy",
          }),
        );
      }

      it("Save is disabled and shows its busy label while in flight, and is enabled again after a failure", async () => {
        const user = userEvent.setup();
        const reply = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "setTicketRoles").mockReturnValue(reply.promise);
        render(<TicketDetail ticketId={5} />);

        await pickDeploy(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
        expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();

        await act(async () => reply.reject(new ApiError(422, "role ids must be valid uuids")));

        expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
        expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
      });

      it("shows the server's message, clears it for a retry, and shows the saved roles", async () => {
        const user = userEvent.setup();
        const retry = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "setTicketRoles")
          .mockRejectedValueOnce(new ApiError(422, "role ids must be valid uuids"))
          .mockReturnValueOnce(retry.promise);
        render(<TicketDetail ticketId={5} />);

        await pickDeploy(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("role ids must be valid uuids");

        await user.click(screen.getByRole("button", { name: "Save" }));
        noAlert();

        await act(async () =>
          retry.resolve(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" })),
        );
        noAlert();
        expect(await screen.findByText("role: deploy")).toBeInTheDocument();
        expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
      });

      // The editor locks while a save is in flight, so the only way to a
      // second save of the same ticket is to leave it and come back; the
      // first reply then arrives last and must not win.
      it("settles on the later of two saves, whichever reply arrives last", async () => {
        const user = userEvent.setup();
        vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
          makeAgentRole({ id: "r1", name: "deploy" }),
          makeAgentRole({ id: "r2", name: "review" }),
        ]);
        const first = deferred<ReturnType<typeof makeTicket>>();
        const second = deferred<ReturnType<typeof makeTicket>>();
        vi.spyOn(apiClient, "setTicketRoles").mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await pickDeploy(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        await switchTo(rerender, 6);
        await switchTo(rerender, 5);

        await user.click(await screen.findByRole("button", { name: "Edit roles" }));
        await user.click(
          within(screen.getByRole("group", { name: "Roles needed to claim this" })).getByRole("checkbox", {
            name: "review",
          }),
        );
        await user.click(screen.getByRole("button", { name: "Save" }));

        await act(async () =>
          second.resolve(makeTicket({ id: 5, required_roles: [{ id: "r2", name: "review" }], role_match: "any" })),
        );
        expect(await screen.findByText("role: review")).toBeInTheDocument();

        await act(async () =>
          first.resolve(makeTicket({ id: 5, required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" })),
        );

        expect(screen.getByText("role: review")).toBeInTheDocument();
        expect(screen.queryByText("role: deploy")).not.toBeInTheDocument();
      });

      it("closes the editor, and drops its error, when the selected ticket changes", async () => {
        const user = userEvent.setup();
        vi.spyOn(apiClient, "setTicketRoles").mockRejectedValue(new ApiError(422, "role ids must be valid uuids"));
        const { rerender } = render(<TicketDetail ticketId={5} />);

        await pickDeploy(user);
        await user.click(screen.getByRole("button", { name: "Save" }));
        await screen.findByRole("alert");

        await switchTo(rerender, 6);

        noAlert();
        expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Edit roles" })).toBeInTheDocument();
      });
    });
  });
});
