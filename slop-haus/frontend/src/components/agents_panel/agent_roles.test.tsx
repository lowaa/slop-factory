import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, RoleInUseError } from "@/api";
import { makeAgentRole } from "@/test/fixtures";
import AgentRoles from "./agent_roles";

describe("AgentRoles", () => {
  it("shows a loading state", () => {
    render(<AgentRoles state={{ status: "loading" }} onChanged={vi.fn()} />);
    expect(screen.getByText("Loading roles…")).toBeInTheDocument();
  });

  it("shows an error state", () => {
    render(<AgentRoles state={{ status: "error", message: "could not reach the server" }} onChanged={vi.fn()} />);
    expect(screen.getByRole("alert")).toHaveTextContent("could not reach the server");
  });

  it("lists each role's name and description", () => {
    render(
      <AgentRoles
        state={{
          status: "loaded",
          roles: [
            makeAgentRole({ id: "r1", name: "reviewer", description: "reviews PRs" }),
            makeAgentRole({ id: "r2", name: "closer", description: "" }),
          ],
        }}
        onChanged={vi.fn()}
      />,
    );

    expect(screen.getByText("reviewer")).toBeInTheDocument();
    expect(screen.getByText("reviews PRs")).toBeInTheDocument();
    expect(screen.getByText("closer")).toBeInTheDocument();
  });

  it("shows an empty state when there are no roles", () => {
    render(<AgentRoles state={{ status: "loaded", roles: [] }} onChanged={vi.fn()} />);
    expect(screen.getByText("No roles yet.")).toBeInTheDocument();
  });

  describe("creating a role", () => {
    it("creates the role with a trimmed name and description, then reports the change", async () => {
      const user = userEvent.setup();
      const create = vi.spyOn(apiClient, "createAgentRole").mockResolvedValue(makeAgentRole({ name: "reviewer" }));
      const onChanged = vi.fn();

      render(<AgentRoles state={{ status: "loaded", roles: [] }} onChanged={onChanged} />);
      await user.type(screen.getByLabelText("Role name"), "  reviewer  ");
      await user.type(screen.getByLabelText("Description (optional)"), "  reviews PRs  ");
      await user.click(screen.getByRole("button", { name: "Add role" }));

      expect(create).toHaveBeenCalledWith("reviewer", "reviews PRs");
      expect(onChanged).toHaveBeenCalledTimes(1);
    });

    it("asks for a name before calling the server", async () => {
      const user = userEvent.setup();
      const create = vi.spyOn(apiClient, "createAgentRole");

      render(<AgentRoles state={{ status: "loaded", roles: [] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Add role" }));

      expect(create).not.toHaveBeenCalled();
      expect(await screen.findByRole("alert")).toHaveTextContent("name");
    });

    it("surfaces the server's refusal", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "createAgentRole").mockRejectedValue(new ApiError(422, "name already taken"));

      render(<AgentRoles state={{ status: "loaded", roles: [] }} onChanged={vi.fn()} />);
      await user.type(screen.getByLabelText("Role name"), "reviewer");
      await user.click(screen.getByRole("button", { name: "Add role" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("name already taken");
    });
  });

  describe("editing a role", () => {
    it("updates the name and description and reports the change", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer", description: "reviews PRs" });
      const update = vi.spyOn(apiClient, "updateAgentRole").mockResolvedValue({ ...role, name: "senior reviewer" });
      const onChanged = vi.fn();

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={onChanged} />);
      await user.click(screen.getByRole("button", { name: "Edit" }));
      const nameInput = screen.getByLabelText("Name");
      await user.clear(nameInput);
      await user.type(nameInput, "senior reviewer");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(update).toHaveBeenCalledWith("r1", { name: "senior reviewer", description: "reviews PRs" });
      expect(onChanged).toHaveBeenCalledTimes(1);
    });

    it("cancel leaves the role untouched and calls nothing", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      const update = vi.spyOn(apiClient, "updateAgentRole");

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Edit" }));
      await user.click(screen.getByRole("button", { name: "Cancel" }));

      expect(update).not.toHaveBeenCalled();
      expect(screen.getByText("reviewer")).toBeInTheDocument();
      expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
    });

    it("surfaces an update failure without closing the editor", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      vi.spyOn(apiClient, "updateAgentRole").mockRejectedValue(new ApiError(422, "name already taken"));

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Edit" }));
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("name already taken");
      expect(screen.getByLabelText("Name")).toBeInTheDocument();
    });
  });

  describe("deleting a role", () => {
    it("requires the confirm before calling deleteAgentRole", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      const del = vi.spyOn(apiClient, "deleteAgentRole");

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Delete" }));

      expect(del).not.toHaveBeenCalled();
      const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
      expect(confirm).toHaveTextContent("Delete role reviewer? It is taken away from every agent that has it.");
    });

    it("confirming deletes the role and reports the change", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      const del = vi.spyOn(apiClient, "deleteAgentRole").mockResolvedValue(undefined);
      const onChanged = vi.fn();

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={onChanged} />);
      await user.click(screen.getByRole("button", { name: "Delete" }));
      const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
      await user.click(within(confirm).getByRole("button", { name: "Delete" }));

      expect(del).toHaveBeenCalledWith("r1", false);
      expect(onChanged).toHaveBeenCalledTimes(1);
    });

    it("cancel leaves the role in place and calls nothing", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      const del = vi.spyOn(apiClient, "deleteAgentRole");

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Delete" }));
      const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
      await user.click(within(confirm).getByRole("button", { name: "Cancel" }));

      expect(del).not.toHaveBeenCalled();
      expect(screen.queryByRole("group", { name: "Confirm delete role reviewer" })).not.toBeInTheDocument();
      expect(screen.getByText("reviewer")).toBeInTheDocument();
    });

    it("surfaces a delete failure without dropping the role", async () => {
      const user = userEvent.setup();
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      vi.spyOn(apiClient, "deleteAgentRole").mockRejectedValue(new ApiError(404, "no such role"));

      render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Delete" }));
      const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
      await user.click(within(confirm).getByRole("button", { name: "Delete" }));

      expect(await within(confirm).findByRole("alert")).toHaveTextContent("no such role");
      expect(screen.getByText("reviewer")).toBeInTheDocument();
    });

    describe("when tickets still require it", () => {
      const role = makeAgentRole({ id: "r1", name: "reviewer" });
      const tickets = [
        { id: 4, title: "Deploy it", status: "open" as const },
        { id: 9, title: "Review the change", status: "open" as const },
      ];

      it("offers to remove it from the tickets and delete anyway, naming them", async () => {
        const user = userEvent.setup();
        vi.spyOn(apiClient, "deleteAgentRole").mockRejectedValue(
          new RoleInUseError("2 ticket(s) require this role.", tickets),
        );

        render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
        await user.click(screen.getByRole("button", { name: "Delete" }));
        const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
        await user.click(within(confirm).getByRole("button", { name: "Delete" }));

        expect(await within(confirm).findByText("#4 Deploy it")).toBeInTheDocument();
        expect(within(confirm).getByText("#9 Review the change")).toBeInTheDocument();
        expect(
          within(confirm).getByRole("button", { name: "Remove from 2 tickets and delete" }),
        ).toBeInTheDocument();
        // The plain "Delete" button is gone -- it already told the server
        // to delete it once, and got this choice back instead.
        expect(within(confirm).queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
      });

      it("removes it from the tickets and deletes it when confirmed", async () => {
        const user = userEvent.setup();
        vi.spyOn(apiClient, "deleteAgentRole").mockRejectedValueOnce(
          new RoleInUseError("2 ticket(s) require this role.", tickets),
        );
        vi.mocked(apiClient.deleteAgentRole).mockResolvedValueOnce(undefined);
        const onChanged = vi.fn();

        render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={onChanged} />);
        await user.click(screen.getByRole("button", { name: "Delete" }));
        const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
        await user.click(within(confirm).getByRole("button", { name: "Delete" }));
        await user.click(await within(confirm).findByRole("button", { name: "Remove from 2 tickets and delete" }));

        expect(apiClient.deleteAgentRole).toHaveBeenNthCalledWith(1, "r1", false);
        expect(apiClient.deleteAgentRole).toHaveBeenNthCalledWith(2, "r1", true);
        expect(onChanged).toHaveBeenCalledTimes(1);
      });

      it("cancelling the conflict choice calls nothing and leaves the role in place", async () => {
        const user = userEvent.setup();
        vi.spyOn(apiClient, "deleteAgentRole").mockRejectedValue(
          new RoleInUseError("2 ticket(s) require this role.", tickets),
        );

        render(<AgentRoles state={{ status: "loaded", roles: [role] }} onChanged={vi.fn()} />);
        await user.click(screen.getByRole("button", { name: "Delete" }));
        const confirm = screen.getByRole("group", { name: "Confirm delete role reviewer" });
        await user.click(within(confirm).getByRole("button", { name: "Delete" }));
        await within(confirm).findByRole("button", { name: "Remove from 2 tickets and delete" });
        await user.click(within(confirm).getByRole("button", { name: "Cancel" }));

        expect(apiClient.deleteAgentRole).toHaveBeenCalledTimes(1);
        expect(screen.queryByRole("group", { name: "Confirm delete role reviewer" })).not.toBeInTheDocument();
        expect(screen.getByText("reviewer")).toBeInTheDocument();
      });
    });
  });
});
