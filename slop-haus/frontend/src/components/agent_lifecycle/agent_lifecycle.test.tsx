import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Agent } from "@/api";
import { makeAgent } from "@/test/fixtures";
import { AgentLifecycleActions, AgentLifecycleFeedback, AgentStateBadge, ShowRemovedToggle, useAgentLifecycle } from ".";

const OFF = "2026-01-02T00:00:00Z";

// Holds the agent the way a host does: each step's result replaces it.
function Harness({ initial }: { initial: Agent }) {
  const [agent, setAgent] = useState(initial);
  const lifecycle = useAgentLifecycle(agent, setAgent);
  return (
    <div>
      <AgentStateBadge agent={agent} />
      <AgentLifecycleActions lifecycle={lifecycle} />
      <AgentLifecycleFeedback lifecycle={lifecycle} />
    </div>
  );
}

afterEach(() => vi.restoreAllMocks());

describe("agent lifecycle", () => {
  it.each([
    ["Enabled", makeAgent({ name: "a" }), ["Disable", "Remove"]],
    ["Disabled", makeAgent({ name: "a", disabled_at: OFF }), ["Enable", "Remove"]],
    ["Removed", makeAgent({ name: "a", disabled_at: OFF, archived_at: OFF }), ["Restore"]],
  ])("shows %s with its actions", (badge, agent, buttons) => {
    render(<Harness initial={agent} />);
    expect(screen.getByText(badge)).toBeInTheDocument();
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(buttons);
  });

  it("asks before removing, and cancel changes nothing", async () => {
    const user = userEvent.setup();
    const disable = vi.spyOn(apiClient, "setAgentDisabled");
    render(<Harness initial={makeAgent({ name: "a" })} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(screen.getByRole("group", { name: "Confirm removing a" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("group")).not.toBeInTheDocument();
    expect(disable).not.toHaveBeenCalled();
  });

  it("skips the disable for an agent that is already disabled", async () => {
    const user = userEvent.setup();
    const agent = makeAgent({ id: "a1", name: "a", disabled_at: OFF });
    const disable = vi.spyOn(apiClient, "setAgentDisabled");
    vi.spyOn(apiClient, "archiveAgent").mockResolvedValue({ ...agent, archived_at: OFF });
    render(<Harness initial={agent} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    await user.click(within(screen.getByRole("group", { name: "Confirm removing a" })).getByRole("button", { name: "Remove" }));

    expect(await screen.findByText("Removed")).toBeInTheDocument();
    expect(disable).not.toHaveBeenCalled();
  });

  it("leaves the agent disabled when the archive fails, and a retry only archives", async () => {
    const user = userEvent.setup();
    const agent = makeAgent({ id: "a1", name: "a" });
    const disable = vi.spyOn(apiClient, "setAgentDisabled").mockResolvedValue({ ...agent, disabled_at: OFF });
    const archive = vi
      .spyOn(apiClient, "archiveAgent")
      .mockRejectedValueOnce(new ApiError(500, "boom"))
      .mockResolvedValueOnce({ ...agent, disabled_at: OFF, archived_at: OFF });
    render(<Harness initial={agent} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    await user.click(within(screen.getByRole("group", { name: "Confirm removing a" })).getByRole("button", { name: "Remove" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("a is disabled but not removed yet. boom");
    expect(screen.getByText("Disabled")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry remove" }));

    expect(await screen.findByText("Removed")).toBeInTheDocument();
    expect(disable).toHaveBeenCalledTimes(1);
    expect(archive).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("archives only after the disable has succeeded, and controls stay disabled meanwhile", async () => {
    const user = userEvent.setup();
    const agent = makeAgent({ id: "a1", name: "a" });
    let finishDisable!: (a: Agent) => void;
    const disable = vi
      .spyOn(apiClient, "setAgentDisabled")
      .mockReturnValue(new Promise<Agent>((resolve) => (finishDisable = resolve)));
    const archive = vi.spyOn(apiClient, "archiveAgent").mockResolvedValue({ ...agent, disabled_at: OFF, archived_at: OFF });
    render(<Harness initial={agent} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("group", { name: "Confirm removing a" });
    await user.click(within(confirm).getByRole("button", { name: "Remove" }));

    expect(within(confirm).getByRole("button", { name: "Removing…" })).toBeDisabled();
    expect(within(confirm).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Disable" })).toBeDisabled();
    expect(archive).not.toHaveBeenCalled();

    finishDisable({ ...agent, disabled_at: OFF });

    expect(await screen.findByText("Removed")).toBeInTheDocument();
    expect(disable).toHaveBeenCalledTimes(1);
    expect(archive).toHaveBeenCalledTimes(1);
  });

  it("never archives when the disable is refused, and keeps the agent enabled", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "setAgentDisabled").mockRejectedValue(new ApiError(409, "agent is busy"));
    const archive = vi.spyOn(apiClient, "archiveAgent");
    render(<Harness initial={makeAgent({ name: "a" })} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    await user.click(within(screen.getByRole("group", { name: "Confirm removing a" })).getByRole("button", { name: "Remove" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("agent is busy");
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    expect(archive).not.toHaveBeenCalled();
  });

  it("restores a removed agent, which stays disabled", async () => {
    const user = userEvent.setup();
    const agent = makeAgent({ id: "a1", name: "a", disabled_at: OFF, archived_at: OFF });
    const unarchive = vi.spyOn(apiClient, "unarchiveAgent").mockResolvedValue({ ...agent, archived_at: null });
    render(<Harness initial={agent} />);

    await user.click(screen.getByRole("button", { name: "Restore" }));

    expect(unarchive).toHaveBeenCalledWith("a1");
    expect(await screen.findByText("Disabled")).toBeInTheDocument();
  });

  it.each([
    ["an agent's owner", 409, "agent is busy"],
    ["a tenant owner", 403, "only the agent's owner may remove it"],
    ["a member", 403, "owners only"],
  ])("shows the server's refusal for %s", async (_who, status, message) => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "setAgentDisabled").mockRejectedValue(new ApiError(status, message));
    render(<Harness initial={makeAgent({ name: "a" })} />);

    await user.click(screen.getByRole("button", { name: "Disable" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getByText("Enabled")).toBeInTheDocument();
  });

  it("enables a disabled agent", async () => {
    const user = userEvent.setup();
    const agent = makeAgent({ id: "a1", name: "a", disabled_at: OFF });
    const set = vi.spyOn(apiClient, "setAgentDisabled").mockResolvedValue({ ...agent, disabled_at: null });
    render(<Harness initial={agent} />);

    await user.click(screen.getByRole("button", { name: "Enable" }));

    expect(set).toHaveBeenCalledWith("a1", false);
    expect(await screen.findByText("Enabled")).toBeInTheDocument();
  });
});

describe("ShowRemovedToggle", () => {
  it("is absent with nothing removed and reports changes otherwise", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const { rerender } = render(<ShowRemovedToggle count={0} checked={false} onChange={onChange} />);
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();

    rerender(<ShowRemovedToggle count={3} checked={false} onChange={onChange} />);
    await user.click(screen.getByRole("checkbox", { name: "Show removed (3)" }));
    expect(onChange).toHaveBeenCalledWith(true);
  });
});
