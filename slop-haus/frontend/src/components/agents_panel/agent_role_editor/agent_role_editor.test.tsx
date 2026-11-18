import {} from "node:child_process";
import {} from "node:os";
import {} from "node:path";
import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError} from "@/api";
import {
  makeAgent,
  makeAgentRole,
  } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import AgentsPanel from "../agents_panel";
import { stubPanelDefaults} from "../agents_panel_test_support";

// The setup help's harness tab is also called "Claude Code".

stubPanelDefaults();

describe("AgentsPanel roles", () => {
  describe("roles on a roster row", () => {
    // A rename or delete in the Agent Roles section changes what the rows'
    // chips should say, so the panel must refetch the roster too.
    it("refreshes the rows' chips after a role is renamed", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      const before = makeAgentRole({ id: "r1", name: "reviewer" });
      const after = makeAgentRole({ id: "r1", name: "code-reviewer" });
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValueOnce([before]).mockResolvedValue([after]);
      vi.spyOn(apiClient, "listAgents")
        .mockResolvedValueOnce([makeAgent({ id: "a1", name: "builder-1", roles: [before] })])
        .mockResolvedValue([makeAgent({ id: "a1", name: "builder-1", roles: [after] })]);
      vi.spyOn(apiClient, "updateAgentRole").mockResolvedValue(after);

      await renderSignedIn(<AgentsPanel />);
      const row = (await screen.findByText("builder-1")).closest("li")!;
      expect(within(row).getByText("reviewer")).toHaveClass("agents-panel__role-chip");

      await user.click(screen.getByRole("button", { name: "Edit" }));
      const name = screen.getByLabelText("Name");
      await user.clear(name);
      await user.type(name, "code-reviewer");
      await user.click(screen.getByRole("button", { name: "Save" }));

      const renamed = await within(screen.getByText("builder-1").closest("li")!).findByText("code-reviewer");
      expect(renamed).toHaveClass("agents-panel__role-chip");
    });

    it("shows a chip per role, and no chips area for an agent with none", async () => {
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "chippy", roles: [makeAgentRole({ id: "r1", name: "reviewer", description: "reviews PRs" })] }),
        makeAgent({ id: "a2", name: "bare", roles: [] }),
      ]);

      await renderSignedIn(<AgentsPanel />);

      const chippy = (await screen.findByText("chippy")).closest("li")!;
      const chip = within(chippy).getByText("reviewer");
      expect(chip).toHaveClass("agents-panel__role-chip");
      expect(chip).toHaveAttribute("title", "reviews PRs");

      const bare = screen.getByText("bare").closest("li")!;
      expect(bare.querySelector(".agents-panel__role-chips")).toBeNull();
    });

    it("opens the roles editor with the agent's current roles pre-checked", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "reviewer" }),
        makeAgentRole({ id: "r2", name: "closer" }),
      ]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "builder-1", roles: [makeAgentRole({ id: "r1", name: "reviewer" })] }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Edit roles" }));

      const editor = screen.getByRole("group", { name: "Roles for builder-1" });
      expect(within(editor).getByRole("checkbox", { name: "reviewer" })).toBeChecked();
      expect(within(editor).getByRole("checkbox", { name: "closer" })).not.toBeChecked();
    });

    it("toggling and saving calls setAgentRoles with the checked ids and updates the chips from the returned agent", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "reviewer" }),
        makeAgentRole({ id: "r2", name: "closer" }),
      ]);
      const agent = makeAgent({ id: "a1", name: "builder-1", roles: [makeAgentRole({ id: "r1", name: "reviewer" })] });
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([agent]);
      const setAgentRoles = vi.spyOn(apiClient, "setAgentRoles").mockResolvedValue({
        ...agent,
        roles: [makeAgentRole({ id: "r2", name: "closer" })],
      });

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles for builder-1" });
      await user.click(within(editor).getByRole("checkbox", { name: "reviewer" }));
      await user.click(within(editor).getByRole("checkbox", { name: "closer" }));
      await user.click(within(editor).getByRole("button", { name: "Save" }));

      expect(setAgentRoles).toHaveBeenCalledWith("a1", ["r2"]);
      expect(screen.queryByRole("group", { name: "Roles for builder-1" })).not.toBeInTheDocument();
      const row = screen.getByText("builder-1").closest("li")!;
      expect(within(row).getByText("closer")).toBeInTheDocument();
      expect(within(row).queryByText("reviewer")).not.toBeInTheDocument();
    });

    it("cancel closes the editor and calls setAgentRoles for nobody", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      const setAgentRoles = vi.spyOn(apiClient, "setAgentRoles");

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles for builder-1" });
      await user.click(within(editor).getByRole("checkbox", { name: "reviewer" }));
      await user.click(within(editor).getByRole("button", { name: "Cancel" }));

      expect(screen.queryByRole("group", { name: "Roles for builder-1" })).not.toBeInTheDocument();
      expect(setAgentRoles).not.toHaveBeenCalled();
    });

    it("points to Agent Roles below when the tenant has none to pick yet", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Edit roles" }));

      const editor = screen.getByRole("group", { name: "Roles for builder-1" });
      expect(editor).toHaveTextContent("No roles yet");
      expect(within(editor).queryByRole("checkbox")).not.toBeInTheDocument();
    });

    it("surfaces a setAgentRoles failure without closing the editor", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      vi.spyOn(apiClient, "setAgentRoles").mockRejectedValue(new ApiError(422, "no such role in this tenant"));

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Edit roles" }));
      const editor = screen.getByRole("group", { name: "Roles for builder-1" });
      await user.click(within(editor).getByRole("checkbox", { name: "reviewer" }));
      await user.click(within(editor).getByRole("button", { name: "Save" }));

      expect(await within(editor).findByRole("alert")).toHaveTextContent("no such role in this tenant");
      expect(screen.getByRole("group", { name: "Roles for builder-1" })).toBeInTheDocument();
    });
  });
});
