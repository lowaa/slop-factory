import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError, apiClient, type Agent, type DesiredAgentView, type DesiredState } from "@/api";
import { chooseFromDropdown } from "@/test/widgets";
import { makeAgent, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { agentStatus } from "./desired_edits";
import MachineRoster from "./machine_roster";

// The groups with no roster of their own: every entry of a document is a line under its machine.
function Roster({ workers = [] }: { workers?: Agent[] }) {
  return <MachineRoster workers={workers} allWorkers={workers} status={undefined} renderList={(agents, controlsFor) => (agents.length === 0 ? null : <ul>{agents.map((a) => <li key={a.id}>{a.name}{controlsFor?.(a)}</li>)}</ul>)} />;
}

const supervisor = { id: "s1", name: "box", archived_at: null, created_at: "", disabled_at: null, slot_cap: 4, slot_count: 0, last_seen_at: "2026-01-01T00:00:00Z", stale: false };

function view(over: Partial<DesiredAgentView> = {}): DesiredAgentView {
  return {
    id: "a1",
    kind: "slot",
    name: "alpha",
    state: "running",
    generation: 1,
    resume_generation: 0,
    observed: { generation: 1, phase: "running" },
    ...over,
  };
}

function stub(state: DesiredState, flag = true) {
  vi.spyOn(apiClient, "getSetting").mockResolvedValue({ name: "provision_from_board", type: "bool", bool_val: flag } as never);
  vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor]);
  return vi.spyOn(apiClient, "getDesiredState").mockResolvedValue(state);
}

beforeEach(() => {
  vi.restoreAllMocks();
});

const unmanaged = [
  { kind: "slot" as const, name: "orphan", phase: "running", offline: false },
  { kind: "slot" as const, name: "stale", phase: "failed", offline: true },
];

function stubUnmanaged(owner: boolean) {
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: owner, current: true })]);
  return vi.spyOn(apiClient, "listUnmanagedSlots").mockResolvedValue(unmanaged);
}

function worker(name: string, supervisorId?: string): Agent {
  return makeAgent({ id: `w-${name}`, name, supervisor_agent_id: supervisorId });
}

describe("grouping the roster by machine", () => {
  const other = { ...supervisor, id: "s2", name: "laptop" };

  function stubTwo(owner = false, state: DesiredState = { revision: 1, agents: [] }) {
    stub(state);
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor, other]);
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: owner, current: true })]);
    vi.spyOn(apiClient, "listUnmanagedSlots").mockResolvedValue([]);
  }

  it("lists each worker under the machine that runs it, and the rest apart", async () => {
    stubTwo();
    await renderSignedIn(<Roster workers={[worker("alpha", "s1"), worker("gamma", "s2"), worker("by-hand")]} />);
    const box = await screen.findByRole("region", { name: "Workers on box" });
    expect(within(box).getByText("alpha")).toBeTruthy();
    expect(within(box).queryByText("gamma")).toBeNull();
    expect(within(screen.getByRole("region", { name: "Workers on laptop" })).getByText("gamma")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Not on a supervisor" })).getByText("by-hand")).toBeTruthy();
  });

  it("calls a worker of a disabled machine unsupervised, since that machine has no group", async () => {
    stubTwo();
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor, { ...other, disabled_at: "2026-10-02T00:00:00Z" }]);
    await renderSignedIn(<Roster workers={[worker("gamma", "s2")]} />);
    expect(within(await screen.findByRole("region", { name: "Not on a supervisor" })).getByText("gamma")).toBeTruthy();
  });

  it("has no Not on a supervisor group when every worker is on a machine", async () => {
    stubTwo();
    await renderSignedIn(<Roster workers={[worker("alpha", "s1")]} />);
    await screen.findByRole("region", { name: "Workers on box" });
    expect(screen.queryByRole("region", { name: "Not on a supervisor" })).toBeNull();
  });

  it("puts the machine's controls on the worker's own row, not in a second list", async () => {
    stubTwo(false, { revision: 4, agents: [view({ name: "alpha" })] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor]);
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 5, agents: [] });
    await renderSignedIn(<Roster workers={[worker("alpha", "s1")]} />);
    const pause = await screen.findByRole("button", { name: "Pause alpha" });
    await userEvent.click(pause);
    await waitFor(() => expect(put.mock.calls[0][2][0].state).toBe("paused"));
    expect(screen.getAllByText("alpha")).toHaveLength(1);
    expect(pause.closest("li")).toHaveTextContent("alpha");
  });

  it("gives an entry with no worker yet a line of its own, but not a worker the roster filtered out", async () => {
    stubTwo(false, { revision: 1, agents: [view({ id: "a1", name: "hidden" }), view({ id: "a2", name: "new-one" })] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor]);
    await renderSignedIn(
      <MachineRoster workers={[]} allWorkers={[worker("hidden", "s1")]} status={undefined} renderList={() => null} />,
    );
    expect(await screen.findByText("new-one")).toBeTruthy();
    expect(screen.queryByText("hidden")).toBeNull();
  });

  it("shows the machine's name, whether it is polling, and the server's protocol note", async () => {
    stubTwo();
    const status = { offline: true, claimable: 0, last_polled_at: null, slots: [], current_bundle_hash: "", current_protocol: 3, supervisors: [{ agent_id: "s1", agent_name: "box", version: "v1", protocol: 2, protocol_status: "stale" as const, last_polled_at: "2026-10-19T10:00:00Z" }] };
    await renderSignedIn(<MachineRoster workers={[]} allWorkers={[]} status={status} renderList={() => null} />);
    const box = await screen.findByRole("region", { name: "Workers on box" });
    expect(within(box).getByText("offline")).toBeTruthy();
    expect(within(box).getByRole("alert")).toHaveTextContent("box speaks supervisor protocol 2; the server is on 3");
    const laptop = screen.getByRole("region", { name: "Workers on laptop" });
    expect(within(laptop).getByText("offline")).toBeTruthy();
    expect(within(laptop).queryByRole("alert")).toBeNull();
    expect(within(box).queryByText("online")).toBeNull();
  });

  it("says online while the supervisors are polling", async () => {
    stubTwo();
    const status = { offline: false, claimable: 0, last_polled_at: null, slots: [], current_bundle_hash: "", current_protocol: 3, supervisors: [] };
    await renderSignedIn(<MachineRoster workers={[]} allWorkers={[]} status={status} renderList={() => null} />);
    expect(within(await screen.findByRole("region", { name: "Workers on box" })).getByText("online")).toBeTruthy();
  });

  it("keeps the roster, with the error, when the supervisors cannot be read", async () => {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockRejectedValue(new ApiError(500, "down"));
    await renderSignedIn(<Roster workers={[worker("alpha", "s1")]} />);
    expect((await screen.findByRole("alert")).textContent).toMatch(/down/);
    expect(screen.getByText("alpha")).toBeTruthy();
  });

  it("writes to the machine whose Add worker was used", async () => {
    stubTwo();
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 2, agents: [] });
    await renderSignedIn(<Roster />);
    const laptop = await screen.findByRole("region", { name: "Workers on laptop" });
    await waitFor(() => expect(within(laptop).getByRole("button", { name: "Add worker" })).toBeTruthy());
    await userEvent.type(within(laptop).getByLabelText("Name"), "delta");
    expect(within(screen.getByRole("region", { name: "Workers on box" })).getByLabelText("Name")).toHaveValue("");
    await userEvent.click(within(laptop).getByRole("button", { name: "Add worker" }));
    await waitFor(() => expect(put.mock.calls[0][0]).toBe("s2"));
  });

  it("keeps the typed name when the write is refused", async () => {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "putDesiredState").mockRejectedValue(new ApiError(400, "name taken"));
    await renderSignedIn(<Roster />);
    await userEvent.type(await screen.findByLabelText("Name"), "delta");
    await userEvent.click(screen.getByRole("button", { name: "Add worker" }));
    expect(await screen.findByText("name taken")).toBeTruthy();
    expect(screen.getByLabelText("Name")).toHaveValue("delta");
  });

  it("offers the header controls to the owner", async () => {
    stubTwo(true);
    await renderSignedIn(<Roster />);
    expect(await screen.findByRole("button", { name: "Change cap for box" })).toBeTruthy();
  });

  it("offers a member no header controls", async () => {
    stubTwo(false);
    await renderSignedIn(<Roster />);
    await screen.findByRole("region", { name: "Workers on box" });
    await waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /Change cap/ })).toBeNull();
  });

  it("points at the setup steps when no machine runs workers", async () => {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([]);
    await renderSignedIn(<Roster workers={[worker("by-hand")]} />);
    expect(await screen.findByText(/No machine runs workers yet/)).toBeTruthy();
    expect(screen.getByText("by-hand")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Not on a supervisor" })).toBeNull();
  });
});

describe("the worker cap", () => {
  function stubOwner(owner: boolean, over: Partial<typeof supervisor> = {}) {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([{ ...supervisor, ...over }]);
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: owner, current: true })]);
    vi.spyOn(apiClient, "listUnmanagedSlots").mockResolvedValue([]);
  }

  it("shows how many workers there are of the cap", async () => {
    stubOwner(false, { slot_count: 3 });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText("3 of 4 workers")).toBeTruthy();
    expect(screen.queryByRole("note")).toBeNull();
  });

  it("says so when the supervisor has more workers than its cap", async () => {
    stubOwner(false, { slot_count: 5, slot_cap: 2 });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText("5 of 2 workers")).toBeTruthy();
    expect(screen.getByRole("note").textContent).toMatch(/more workers than its cap/);
  });

  it("offers no edit to a member", async () => {
    stubOwner(false);
    await renderSignedIn(<Roster />);
    await screen.findByText("0 of 4 workers");
    expect(screen.queryByRole("button", { name: /Change cap/ })).toBeNull();
  });

  it("saves the owner's new cap and re-reads the supervisors", async () => {
    stubOwner(true);
    const set = vi.spyOn(apiClient, "setSupervisorSlotCap").mockResolvedValue({ ...supervisor, slot_cap: 6 });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Change cap for box" }));
    const input = screen.getByLabelText("Workers box may run at once");
    await userEvent.clear(input);
    await userEvent.type(input, "6");
    vi.mocked(apiClient.listProvisionSupervisors).mockResolvedValue([{ ...supervisor, slot_cap: 6 }]);
    await userEvent.click(screen.getByRole("button", { name: "Save cap" }));

    await waitFor(() => expect(set).toHaveBeenCalledWith("s1", 6));
    expect(await screen.findByText("0 of 6 workers")).toBeTruthy();
    expect(screen.queryByLabelText("Workers box may run at once")).toBeNull();
  });

  it("will not send a cap the server would refuse", async () => {
    stubOwner(true);
    const set = vi.spyOn(apiClient, "setSupervisorSlotCap");
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Change cap for box" }));
    const input = screen.getByLabelText("Workers box may run at once");
    await userEvent.clear(input);
    await userEvent.type(input, "17");
    expect((screen.getByRole("button", { name: "Save cap" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("note").textContent).toMatch(/1 to 16/);
    expect(set).not.toHaveBeenCalled();
  });

  it("shows the server's refusal and keeps the editor open", async () => {
    stubOwner(true);
    vi.spyOn(apiClient, "setSupervisorSlotCap").mockRejectedValue(new ApiError(403, "only the team's owner may do that"));
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Change cap for box" }));
    const input = screen.getByLabelText("Workers box may run at once");
    await userEvent.clear(input);
    await userEvent.type(input, "5");
    await userEvent.click(screen.getByRole("button", { name: "Save cap" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/only the team's owner/);
    expect(screen.getByLabelText("Workers box may run at once")).toBeTruthy();
  });
});

describe("a stale supervisor", () => {
  function stubStale(owner: boolean, stale = true) {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([{ ...supervisor, stale }]);
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: owner, current: true })]);
    vi.spyOn(apiClient, "listUnmanagedSlots").mockResolvedValue([]);
  }

  it("offers the owner to end it, and re-reads the supervisors once confirmed", async () => {
    stubStale(true);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const end = vi.spyOn(apiClient, "setSupervisorDisabled").mockResolvedValue({ ...supervisor, disabled_at: "2026-01-02T00:00:00Z" });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/Not heard from since/)).toBeTruthy();
    vi.mocked(apiClient.listProvisionSupervisors).mockResolvedValue([{ ...supervisor, disabled_at: "2026-01-02T00:00:00Z" }]);
    await userEvent.click(screen.getByRole("button", { name: "End supervisor box" }));
    await waitFor(() => expect(end).toHaveBeenCalledWith("s1", true));
    await waitFor(() => expect(screen.queryByText("box")).toBeNull());
  });

  it("ends nothing when the owner declines the confirmation", async () => {
    stubStale(true);
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const end = vi.spyOn(apiClient, "setSupervisorDisabled");
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "End supervisor box" }));
    expect(end).not.toHaveBeenCalled();
  });

  it("shows a member the notice but not the button", async () => {
    stubStale(false);
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/Not heard from since/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /End supervisor/ })).toBeNull();
  });

  it("offers nothing for a supervisor that is not stale", async () => {
    stubStale(true, false);
    await renderSignedIn(<Roster />);
    await screen.findByText("0 of 4 workers");
    expect(screen.queryByText(/Not heard from since/)).toBeNull();
    expect(screen.queryByRole("button", { name: /End supervisor/ })).toBeNull();
  });
});

describe("MachineRoster", () => {
  it("shows nothing while provision_from_board is off", async () => {
    const get = stub({ revision: 1, agents: [view()] }, false);
    await renderSignedIn(<Roster workers={[worker("alpha", "s1")]} />);
    await waitFor(() => expect(apiClient.getSetting).toHaveBeenCalled());
    // The plain roster, with no machine around it.
    expect(await screen.findByText("alpha")).toBeTruthy();
    expect(screen.queryByRole("region")).toBeNull();
    expect(screen.queryByRole("button", { name: /Pause|Add worker/ })).toBeNull();
    expect(get).not.toHaveBeenCalled();
  });

  it("adds an agent by writing the whole document on the revision it read", async () => {
    stub({ revision: 7, agents: [view()] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 8, agents: [view()] });
    await renderSignedIn(<Roster />);
    await userEvent.type(await screen.findByLabelText("Name"), "beta");
    await userEvent.click(screen.getByRole("button", { name: "Add worker" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith("s1", 7, [
        { id: "a1", kind: "slot", name: "alpha", harness: undefined, state: "running", resume_generation: 0 },
        { kind: "slot", name: "beta", state: "running", resume_generation: 0 },
      ]),
    );
  });

  it("pauses and retires from the agent's row", async () => {
    stub({ revision: 2, agents: [view()] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 3, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Pause alpha" }));
    await waitFor(() => expect(put.mock.calls[0][2][0].state).toBe("paused"));
    await userEvent.click(screen.getByRole("button", { name: "Retire alpha" }));
    await waitFor(() => expect(put.mock.calls[1][2][0].state).toBe("removed"));
  });

  it("resumes an owner hold by setting the agent running", async () => {
    stub({ revision: 2, agents: [view({ state: "paused", observed: { generation: 1, phase: "paused" } })] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 3, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Resume alpha" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ state: "running", resume_generation: 0 }));
  });

  it("shows the observed harness only while it differs from the desired one", async () => {
    stub({ revision: 2, agents: [view({ harness: "pi", observed: { generation: 1, phase: "running", harness: "claude" } })] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText("running claude")).toBeTruthy();
  });

  it("shows no observed harness once it matches", async () => {
    stub({ revision: 2, agents: [view({ harness: "pi", observed: { generation: 1, phase: "running", harness: "pi" } })] });
    await renderSignedIn(<Roster />);
    await screen.findByText("alpha");
    expect(screen.queryByText(/^running (pi|claude)$/)).toBeNull();
  });

  it("compares against the default harness when the desired one is omitted", async () => {
    stub({ revision: 2, agents: [view({ observed: { generation: 1, phase: "running", harness: "claude" } })] });
    await renderSignedIn(<Roster />);
    await screen.findByText("alpha");
    expect(screen.queryByText("running claude")).toBeNull();
  });

  it("leaves out archived supervisors", async () => {
    stub({ revision: 1, agents: [] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([{ ...supervisor, archived_at: "2026-10-02T00:00:00Z" }]);
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/No machine runs workers yet/)).toBeTruthy();
  });

  it("leaves out disabled supervisors, whose removed agents would read Retiring forever", async () => {
    stub({ revision: 1, agents: [{ id: "a1", kind: "slot", name: "gone", state: "removed", resume_generation: 0, generation: 2, observed: { generation: 2, phase: "provisioning" } }] });
    vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([{ ...supervisor, disabled_at: "2026-10-02T00:00:00Z" }]);
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/No machine runs workers yet/)).toBeTruthy();
    expect(screen.queryByText("Retiring")).toBeNull();
  });

  it("still shows Retiring for a removed agent on an enabled supervisor", async () => {
    stub({ revision: 1, agents: [{ id: "a1", kind: "slot", name: "gone", state: "removed", resume_generation: 0, generation: 2, observed: { generation: 2, phase: "provisioning" } }] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText("Retiring")).toBeTruthy();
  });

  it("raises resume_generation to clear the supervisor's own pause", async () => {
    stub({ revision: 2, agents: [view({ observed: { generation: 1, phase: "paused", reason: "quota" } })] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 3, agents: [] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/Paused by the supervisor: quota/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Clear pause on alpha" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ state: "running", resume_generation: 1 }));
  });

  it("reads the document again after a stale-revision conflict", async () => {
    const get = stub({ revision: 2, agents: [view()] });
    vi.spyOn(apiClient, "putDesiredState").mockRejectedValue(
      new ApiError(409, "the desired state changed since you read it: read it again and retry"),
    );
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Pause alpha" }));
    expect(await screen.findByText(/list was read again/)).toBeTruthy();
    expect(get.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("shows the one command for a Remote Control identity", async () => {
    stub({ revision: 1, agents: [view({ kind: "remote_control", name: "laptop", launch: "manual" })] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(".slop-haus/run_remote_control.sh")).toBeTruthy();
  });
});

describe("Remote Control agents", () => {
  const rc = (over: Partial<DesiredAgentView> = {}) => view({ kind: "remote_control", name: "laptop", launch: "supervisor", ...over });

  it("adds one launched by the supervisor by default", async () => {
    stub({ revision: 1, agents: [] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 2, agents: [] });
    await renderSignedIn(<Roster />);
    await screen.findByLabelText("Name");
    await chooseFromDropdown(userEvent.setup(), "Kind", "Remote Control");
    await userEvent.type(screen.getByLabelText("Name"), "desk");
    await userEvent.click(screen.getByRole("button", { name: "Add worker" }));
    await waitFor(() =>
      expect(put.mock.calls[0][2]).toEqual([{ kind: "remote_control", name: "desk", state: "running", resume_generation: 0, launch: "supervisor" }]),
    );
  });

  it("adds one run by hand when launch is set to manual", async () => {
    stub({ revision: 1, agents: [] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 2, agents: [] });
    await renderSignedIn(<Roster />);
    await screen.findByLabelText("Name");
    const user = userEvent.setup();
    await chooseFromDropdown(user, "Kind", "Remote Control");
    await chooseFromDropdown(user, "Launch", "Run by hand");
    await user.type(screen.getByLabelText("Name"), "desk");
    await user.click(screen.getByRole("button", { name: "Add worker" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ launch: "manual" }));
  });

  it("warns about pi before a pi Remote Control is added, and sends the harness", async () => {
    stub({ revision: 1, agents: [] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 2, agents: [] });
    await renderSignedIn(<Roster />);
    await screen.findByLabelText("Name");
    const user = userEvent.setup();
    await chooseFromDropdown(user, "Kind", "Remote Control");
    expect(screen.queryByText(/never asks before running a tool/)).toBeNull();
    await chooseFromDropdown(user, "Harness", "pi");
    expect(screen.getByText(/never asks before running a tool/)).toBeTruthy();
    await user.type(screen.getByLabelText("Name"), "desk");
    await user.click(screen.getByRole("button", { name: "Add worker" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ harness: "pi", launch: "supervisor" }));
  });

  it("changes an existing slot's harness by writing the desired state", async () => {
    stub({ revision: 5, agents: [view({ harness: "claude" })] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 6, agents: [] });
    await renderSignedIn(<Roster />);
    await screen.findByText("alpha");
    const user = userEvent.setup();
    expect(screen.queryByText(/never asks before running a tool/)).toBeNull();
    await chooseFromDropdown(user, "Harness for alpha", "Codex");
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][1]).toBe(5);
    expect(put.mock.calls[0][2][0]).toMatchObject({ id: "a1", harness: "codex", state: "running" });
  });

  it("warns about pi on a slot that runs it", async () => {
    stub({ revision: 5, agents: [view({ harness: "pi" })] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/never asks before running a tool/)).toBeTruthy();
  });

  it("shows no command for a supervisor-launched agent, and offers to run it by hand", async () => {
    stub({ revision: 3, agents: [rc()] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 4, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Run laptop by hand" }));
    expect(screen.queryByText(".slop-haus/run_remote_control.sh")).toBeNull();
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ id: "a1", launch: "manual" }));
  });

  it("adopts a manual agent by switching its launch to the supervisor", async () => {
    stub({ revision: 3, agents: [rc({ launch: "" })] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 4, agents: [] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(".slop-haus/run_remote_control.sh")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Let the supervisor run laptop" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ launch: "supervisor" }));
  });

  it("keeps an agent's launch when another edit rewrites the document", async () => {
    stub({ revision: 3, agents: [rc()] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 4, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Pause laptop" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ state: "paused", launch: "supervisor" }));
  });

  it("says the workspace is out of date while a session is live", async () => {
    stub({ revision: 1, agents: [rc({ observed: { generation: 1, phase: "running", reason: "workspace_out_of_date" } })] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/workspace out of date; will sync when idle/)).toBeTruthy();
    expect(agentStatus(rc({ observed: { generation: 1, phase: "running", reason: "workspace_out_of_date: manifest r12" } })).kind).toBe("workspace_out_of_date");
    expect(agentStatus(rc({ observed: { generation: 1, phase: "running", reason: "other" } })).kind).toBe("running");
  });

  it("shows a crash loop with its reason and lets the owner clear it", async () => {
    stub({ revision: 1, agents: [rc({ observed: { generation: 1, phase: "paused", reason: "crash_loop: the host exited 5 times" } })] });
    const put = vi.spyOn(apiClient, "putDesiredState").mockResolvedValue({ revision: 2, agents: [] });
    await renderSignedIn(<Roster />);
    expect(await screen.findByText(/Crash loop: crash_loop: the host exited 5 times/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Clear pause on laptop" }));
    await waitFor(() => expect(put.mock.calls[0][2][0]).toMatchObject({ resume_generation: 1 }));
  });
});

describe("slots not managed by the board", () => {
  it("lists every reported slot with its kind and phase", async () => {
    stub({ revision: 1, agents: [] });
    stubUnmanaged(true);
    await renderSignedIn(<Roster />);
    expect(await screen.findByText("orphan")).toBeTruthy();
    expect(screen.getAllByText("slot")).toHaveLength(2);
    expect(screen.getByText("stale")).toBeTruthy();
    expect(screen.getByText("failed (offline)")).toBeTruthy();
  });

  it("does not ask for the list when the user is not the owner", async () => {
    stub({ revision: 1, agents: [] });
    const list = stubUnmanaged(false);
    await renderSignedIn(<Roster />);
    await screen.findByLabelText("Name");
    expect(screen.queryByText("orphan")).toBeNull();
    expect(list).not.toHaveBeenCalled();
  });

  it("imports one slot on the revision it read, then reads again", async () => {
    const get = stub({ revision: 5, agents: [] });
    const list = stubUnmanaged(true);
    const imp = vi.spyOn(apiClient, "importSupervisorSlots").mockResolvedValue({ revision: 6, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Import orphan" }));
    await waitFor(() => expect(imp).toHaveBeenCalledWith("s1", 5, ["orphan"]));
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThanOrEqual(2));
    expect(list.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("imports all of them in one call", async () => {
    stub({ revision: 5, agents: [] });
    stubUnmanaged(true);
    const imp = vi.spyOn(apiClient, "importSupervisorSlots").mockResolvedValue({ revision: 6, agents: [] });
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Import all" }));
    await waitFor(() => expect(imp).toHaveBeenCalledWith("s1", 5, ["orphan", "stale"]));
  });

  it("reads again after a stale-revision conflict", async () => {
    const get = stub({ revision: 5, agents: [] });
    const list = stubUnmanaged(true);
    vi.spyOn(apiClient, "importSupervisorSlots").mockRejectedValue(
      new ApiError(409, "the desired state changed since you read it: read it again and retry"),
    );
    await renderSignedIn(<Roster />);
    await userEvent.click(await screen.findByRole("button", { name: "Import all" }));
    expect(await screen.findByText(/list was read again/)).toBeTruthy();
    expect(get.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(list.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});

describe("agentStatus", () => {
  it("is waiting until the supervisor has reported the current generation", () => {
    expect(agentStatus(view({ generation: 2, observed: { generation: 1, phase: "running" } })).kind).toBe("waiting");
    expect(agentStatus(view({ observed: { generation: 0 } })).kind).toBe("waiting");
  });

  it("carries the reason of a failure at the current generation", () => {
    expect(agentStatus(view({ observed: { generation: 1, phase: "failed", reason: "no disk" } }))).toEqual({ kind: "failed", reason: "no disk" });
  });

  it("tells an owner hold from the supervisor's own pause", () => {
    const paused = { generation: 1, phase: "paused" as const };
    expect(agentStatus(view({ state: "paused", observed: paused })).kind).toBe("owner_hold");
    expect(agentStatus(view({ observed: paused })).kind).toBe("supervisor_pause");
  });

  it("retires only once the supervisor says so", () => {
    expect(agentStatus(view({ state: "removed" })).kind).toBe("retiring");
    expect(agentStatus(view({ state: "removed", observed: { generation: 1, phase: "retired" } })).kind).toBe("retired");
  });

  it("shows why a removal was refused", () => {
    const refused = view({ state: "removed", observed: { generation: 1, phase: "failed", reason: "unpushed work" } });
    expect(agentStatus(refused)).toEqual({ kind: "failed", reason: "unpushed work" });
    const stale = view({ state: "removed", generation: 2, observed: { generation: 1, phase: "failed", reason: "old" } });
    expect(agentStatus(stale).kind).toBe("retiring");
  });
});