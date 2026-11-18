import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeConductor, makeConversation, makeHarnessConfig } from "@/test/fixtures";
import SessionsPanel from "./sessions_panel";
import { updateAvailable } from "@/lib/use_machines";

const OFF = "2026-01-02T00:00:00Z";

afterEach(() => vi.restoreAllMocks());

function renderPanel(sessions: ReturnType<typeof makeConversation>[], extra: Partial<React.ComponentProps<typeof SessionsPanel>> = {}) {
  render(
    <SessionsPanel
      state={{ status: "loaded", removed: [], machines: [{ conductor: makeConductor({ id: "c-1", name: "laptop" }), online: true, sessions }] }}
      open
      onToggle={vi.fn()}
      sessionId={undefined}
      newOn={undefined}
      onOpenSession={vi.fn()}
      onNewSession={vi.fn()}
      {...extra}
    />,
  );
  return within(screen.getByRole("region", { name: "laptop" }));
}

describe("SessionsPanel names", () => {
  it("puts the name on a second line, in full in the title and the accessible name", () => {
    const long = "refactor the whole of the importer so that it streams rows instead of loading them";
    const group = renderPanel([makeConversation({ id: "s-1", first_line: long })]);

    const item = group.getByRole("button", { name: new RegExp(long) });
    expect(item).toHaveAttribute("title", long);
    expect(within(item).getByText(long)).toHaveClass("sessions-panel__name");
  });

  it("names a session by its custom title, else its first line, else Session", () => {
    const group = renderPanel([
      makeConversation({ id: "s-1", title: "Mine", first_line: "first" }),
      makeConversation({ id: "s-2", first_line: "second" }),
      makeConversation({ id: "s-3" }),
    ]);

    expect(group.getByText("Mine")).toBeInTheDocument();
    expect(group.queryByText("first")).not.toBeInTheDocument();
    expect(group.getByText("second")).toBeInTheDocument();
    expect(group.getByText("Session")).toBeInTheDocument();
  });

  it("leaves the New session row as it was", () => {
    const group = renderPanel([]);

    expect(group.getByRole("button", { name: "+ New session" })).not.toHaveAttribute("title");
  });
});

describe("SessionsPanel agent menu", () => {
  it("holds Edit config, New setup script, Disable and Remove behind the heading's ⋯", async () => {
    const group = renderPanel([]);
    expect(group.queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();

    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));

    expect(group.getAllByRole("button").slice(1).map((b) => b.textContent)).toEqual(
      expect.arrayContaining(["Edit config", "New setup script", "Disable", "Remove"]),
    );
  });

  it("asks before removing, then removes (disable, then archive) and tells the page", async () => {
    const disable = vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue(makeConductor({ id: "c-1", disabled_at: OFF }));
    const archive = vi.spyOn(apiClient, "archiveConductor").mockResolvedValue(makeConductor({ id: "c-1", disabled_at: OFF, archived_at: OFF }));
    const onAgentChanged = vi.fn();
    const group = renderPanel([], { onAgentChanged });

    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(group.getByRole("button", { name: "Remove" }));
    expect(disable).not.toHaveBeenCalled();
    const confirm = group.getByRole("group", { name: "Confirm removing laptop" });
    await userEvent.click(within(confirm).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(onAgentChanged).toHaveBeenCalledOnce());
    expect(disable).toHaveBeenCalledWith("c-1", true);
    expect(archive).toHaveBeenCalledWith("c-1");
  });

  describe("one a supervisor runs", () => {
    const RUN_BY = "s-1";

    function renderRunBy(onAgentChanged = vi.fn()) {
      render(
        <SessionsPanel
          state={{ status: "loaded", removed: [], machines: [{ conductor: makeConductor({ id: "c-1", name: "laptop", run_by_supervisor_id: RUN_BY }), online: true, sessions: [] }] }}
          open
          onToggle={vi.fn()}
          sessionId={undefined}
          newOn={undefined}
          onOpenSession={vi.fn()}
          onNewSession={vi.fn()}
          onAgentChanged={onAgentChanged}
        />,
      );
      return within(screen.getByRole("region", { name: "laptop" }));
    }

    it("offers no setup script: the supervisor holds its credential", async () => {
      const group = renderRunBy();

      await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));

      expect(group.queryByRole("button", { name: "New setup script" })).not.toBeInTheDocument();
      expect(group.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    });

    it("is not offered back once removed: nothing would run it", async () => {
      render(
        <SessionsPanel
          state={{ status: "loaded", machines: [], removed: [makeConductor({ id: "c-1", name: "laptop", disabled_at: OFF, archived_at: OFF, run_by_supervisor_id: RUN_BY })] }}
          open
          onToggle={vi.fn()}
          sessionId={undefined}
          newOn={undefined}
          onOpenSession={vi.fn()}
          onNewSession={vi.fn()}
        />,
      );

      await userEvent.click(screen.getByRole("checkbox", { name: /Show removed/ }));

      expect(screen.getByText("laptop")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
    });

    it("is removed by taking it off its supervisor, not by archiving it", async () => {
      vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue(makeConductor({ id: "c-1", disabled_at: OFF, run_by_supervisor_id: RUN_BY }));
      const archive = vi.spyOn(apiClient, "archiveConductor");
      const remove = vi.spyOn(apiClient, "removeOwnRemoteControl").mockResolvedValue(undefined);
      const retired = makeConductor({ id: "c-1", name: "laptop", disabled_at: OFF, archived_at: OFF, run_by_supervisor_id: RUN_BY });
      vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [retired], others: [] });
      const onAgentChanged = vi.fn();
      const group = renderRunBy(onAgentChanged);

      await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
      await userEvent.click(group.getByRole("button", { name: "Remove" }));
      await userEvent.click(within(group.getByRole("group", { name: "Confirm removing laptop" })).getByRole("button", { name: "Remove" }));

      await waitFor(() => expect(onAgentChanged).toHaveBeenCalledOnce());
      expect(remove).toHaveBeenCalledWith(RUN_BY, "laptop");
      expect(archive).not.toHaveBeenCalled();
    });
  });

  it("lists a disabled agent with no sessions and offers Enable", async () => {
    const enable = vi.spyOn(apiClient, "setConductorDisabled").mockResolvedValue(makeConductor({ id: "c-1" }));
    render(
      <SessionsPanel
        state={{
          status: "loaded",
          removed: [],
          machines: [{ conductor: makeConductor({ id: "c-1", name: "laptop", disabled_at: OFF }), online: null, sessions: [] }],
        }}
        open
        onToggle={vi.fn()}
        sessionId={undefined}
        newOn={undefined}
        onOpenSession={vi.fn()}
        onNewSession={vi.fn()}
      />,
    );
    const group = within(screen.getByRole("region", { name: "laptop" }));

    expect(group.getByText("Disabled")).toBeInTheDocument();
    expect(group.queryByRole("button", { name: "+ New session" })).not.toBeInTheDocument();
    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    expect(group.queryByRole("button", { name: "New setup script" })).not.toBeInTheDocument();
    await userEvent.click(group.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(enable).toHaveBeenCalledWith("c-1", false));
  });

  it("downloads a new setup script only after confirming", async () => {
    const rotate = vi.spyOn(apiClient, "rotateConductorSetupScript").mockRejectedValue(new ApiError(500, "no script today"));
    const group = renderPanel([]);

    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(group.getByRole("button", { name: "New setup script" }));
    expect(rotate).not.toHaveBeenCalled();
    await userEvent.click(group.getByRole("button", { name: "Sign out and download" }));

    expect(await group.findByRole("alert")).toHaveTextContent("no script today");
  });
});

describe("SessionsPanel agent config and setup script", () => {
  it("saves a script it rotated, naming the file", async () => {
    const rotate = vi.spyOn(apiClient, "rotateConductorSetupScript").mockResolvedValue({ filename: "setup-laptop.sh", script: "#!/bin/sh" });
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:x");
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const group = renderPanel([]);

    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(group.getByRole("button", { name: "New setup script" }));
    await userEvent.click(group.getByRole("button", { name: "Sign out and download" }));

    expect(await group.findByRole("status")).toHaveTextContent("setup-laptop.sh");
    expect(rotate).toHaveBeenCalledWith("c-1");
    expect(URL.createObjectURL).toHaveBeenCalled();
  });

  it("resets an override to the tenant defaults and closes the editor", async () => {
    const config = makeHarnessConfig();
    config.overrides = [{ agent_id: "c-1", revision: 1, settings: {}, updated_at: OFF, updated_by: null }];
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(config);
    const remove = vi.spyOn(apiClient, "removeAgentHarnessConfig").mockResolvedValue();
    const group = renderPanel([]);

    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(group.getByRole("button", { name: "Edit config" }));
    await userEvent.click(await group.findByRole("button", { name: "Reset to tenant defaults" }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("c-1"));
    await waitFor(() => expect(group.queryByRole("button", { name: "Reset to tenant defaults" })).not.toBeInTheDocument());
  });
});

describe("SessionsPanel removed agents", () => {
  function renderRemoved(onAgentChanged = vi.fn()) {
    render(
      <SessionsPanel
        state={{
          status: "loaded",
          removed: [makeConductor({ id: "c-9", name: "old-box", disabled_at: OFF, archived_at: OFF })],
          machines: [],
        }}
        open
        onToggle={vi.fn()}
        sessionId={undefined}
        newOn={undefined}
        onOpenSession={vi.fn()}
        onNewSession={vi.fn()}
        onAgentChanged={onAgentChanged}
      />,
    );
  }

  it("keeps a removed agent, and so its sessions, out of the list until Show removed, then restores it", async () => {
    const restore = vi.spyOn(apiClient, "unarchiveConductor").mockResolvedValue(makeConductor({ id: "c-9", disabled_at: OFF }));
    const onAgentChanged = vi.fn();
    renderRemoved(onAgentChanged);

    expect(screen.queryByText("old-box")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Show removed (1)" }));
    expect(screen.getByText("old-box")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "old-box" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Restore" }));
    await waitFor(() => expect(restore).toHaveBeenCalledWith("c-9"));
    await waitFor(() => expect(onAgentChanged).toHaveBeenCalled());
  });

  it("shows no toggle when nothing is removed", () => {
    renderPanel([]);
    expect(screen.queryByText(/Show removed/)).not.toBeInTheDocument();
  });
});

describe("SessionsPanel host version", () => {
  const withBuild = (build?: { version: string; current: string }) => {
    render(
      <SessionsPanel
        state={{
          status: "loaded",
          removed: [],
          machines: [{ conductor: makeConductor({ id: "c-1", name: "laptop" }), online: true, build, sessions: [] }],
        }}
        open
        onToggle={vi.fn()}
        sessionId={undefined}
        newOn={undefined}
        onOpenSession={vi.fn()}
        onNewSession={vi.fn()}
      />,
    );
    return within(screen.getByRole("region", { name: "laptop" }));
  };

  it("shows the host's version, with no hint when it is the one the server ships", () => {
    const group = withBuild({ version: "aaaa11112222", current: "aaaa11112222" });
    expect(group.getByText(/Host version aaaa11112222/)).toBeInTheDocument();
    expect(group.queryByText(/Update available/)).toBeNull();
  });

  it("says an update is available when the host runs another build", () => {
    const group = withBuild({ version: "aaaa11112222", current: "bbbb33334444" });
    expect(group.getByText(/Update available: restart the host to run bbbb33334444/)).toBeInTheDocument();
  });

  it("shows nothing for a host that reports no version, and never calls it behind", () => {
    const group = withBuild({ version: "", current: "bbbb33334444" });
    expect(group.queryByText(/Host version/)).toBeNull();
    expect(updateAvailable({ version: "", current: "bbbb33334444" })).toBe(false);
    expect(updateAvailable({ version: "aaaa11112222", current: "" })).toBe(false);
  });
});
