import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeAgentRole } from "@/test/fixtures";
import RoleRequirementPicker from "./role_requirement_picker";

const roles = [
  makeAgentRole({ id: "r1", name: "reviewer" }),
  makeAgentRole({ id: "r2", name: "deployer" }),
  makeAgentRole({ id: "r3", name: "closer" }),
];

describe("RoleRequirementPicker", () => {
  it("renders nothing when there are no roles and no empty hint", () => {
    const { container } = render(
      <RoleRequirementPicker roles={[]} selectedIds={[]} onSelectionChange={vi.fn()} legend="Roles" />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the empty hint instead of a checklist when there are no roles", () => {
    render(
      <RoleRequirementPicker
        roles={[]}
        selectedIds={[]}
        onSelectionChange={vi.fn()}
        legend="Roles"
        emptyHint="No roles yet."
      />,
    );
    expect(screen.getByText("No roles yet.")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("lists every role as a checkbox, checked per selectedIds", () => {
    render(
      <RoleRequirementPicker roles={roles} selectedIds={["r2"]} onSelectionChange={vi.fn()} legend="Roles needed" />,
    );

    const group = screen.getByRole("group", { name: "Roles needed" });
    expect(within(group).getByRole("checkbox", { name: "reviewer" })).not.toBeChecked();
    expect(within(group).getByRole("checkbox", { name: "deployer" })).toBeChecked();
    expect(within(group).getByRole("checkbox", { name: "closer" })).not.toBeChecked();
  });

  it("adds a role to the selection when its checkbox is checked", async () => {
    const user = userEvent.setup();
    const onSelectionChange = vi.fn();
    render(
      <RoleRequirementPicker roles={roles} selectedIds={["r1"]} onSelectionChange={onSelectionChange} legend="Roles" />,
    );

    await user.click(screen.getByRole("checkbox", { name: "deployer" }));

    expect(onSelectionChange).toHaveBeenCalledWith(["r1", "r2"]);
  });

  it("removes a role from the selection when its checkbox is unchecked", async () => {
    const user = userEvent.setup();
    const onSelectionChange = vi.fn();
    render(
      <RoleRequirementPicker
        roles={roles}
        selectedIds={["r1", "r2"]}
        onSelectionChange={onSelectionChange}
        legend="Roles"
      />,
    );

    await user.click(screen.getByRole("checkbox", { name: "reviewer" }));

    expect(onSelectionChange).toHaveBeenCalledWith(["r2"]);
  });

  it("disables every checkbox when disabled", () => {
    render(<RoleRequirementPicker roles={roles} selectedIds={[]} onSelectionChange={vi.fn()} legend="Roles" disabled />);

    for (const checkbox of screen.getAllByRole("checkbox")) {
      expect(checkbox).toBeDisabled();
    }
  });

  describe("the any/all toggle", () => {
    it("stays hidden with no onMatchModeChange, however many roles are picked", () => {
      render(
        <RoleRequirementPicker roles={roles} selectedIds={["r1", "r2"]} onSelectionChange={vi.fn()} legend="Roles" />,
      );
      expect(screen.queryByText("any of these")).not.toBeInTheDocument();
    });

    it("stays hidden with fewer than two roles picked", () => {
      render(
        <RoleRequirementPicker
          roles={roles}
          selectedIds={["r1"]}
          onSelectionChange={vi.fn()}
          onMatchModeChange={vi.fn()}
          matchMode="any"
          legend="Roles"
        />,
      );
      expect(screen.queryByText("any of these")).not.toBeInTheDocument();
    });

    it("shows once two roles are picked and a handler is given, defaulting to any", () => {
      render(
        <RoleRequirementPicker
          roles={roles}
          selectedIds={["r1", "r2"]}
          onSelectionChange={vi.fn()}
          onMatchModeChange={vi.fn()}
          matchMode="any"
          legend="Roles"
        />,
      );
      expect(screen.getByRole("radio", { name: "any of these" })).toBeChecked();
      expect(screen.getByRole("radio", { name: "all of these" })).not.toBeChecked();
    });

    it("reports the mode when the person picks 'all of these'", async () => {
      const user = userEvent.setup();
      const onMatchModeChange = vi.fn();
      render(
        <RoleRequirementPicker
          roles={roles}
          selectedIds={["r1", "r2"]}
          onSelectionChange={vi.fn()}
          onMatchModeChange={onMatchModeChange}
          matchMode="any"
          legend="Roles"
        />,
      );

      await user.click(screen.getByRole("radio", { name: "all of these" }));

      expect(onMatchModeChange).toHaveBeenCalledWith("all");
    });

    it("disables the radios too when disabled", () => {
      render(
        <RoleRequirementPicker
          roles={roles}
          selectedIds={["r1", "r2"]}
          onSelectionChange={vi.fn()}
          onMatchModeChange={vi.fn()}
          matchMode="any"
          legend="Roles"
          disabled
        />,
      );
      for (const radio of screen.getAllByRole("radio")) {
        expect(radio).toBeDisabled();
      }
    });
  });
});
