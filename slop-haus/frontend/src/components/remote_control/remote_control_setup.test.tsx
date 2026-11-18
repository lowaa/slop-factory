import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type DesiredAgentView, type OwnRemoteControlSupervisor } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import * as navigation from "@/lib/navigation";
import RemoteControlSetup from "./remote_control_setup";

// The scripts the section saves are handed to the browser's download; here
// it only matters that one was.
const saved = vi.hoisted(() => vi.fn());
vi.mock("../agents_panel/save_file", () => ({ saveFile: saved }));

const SCRIPT = { filename: "slop-haus-setup-mine.sh", script: "#!/bin/sh\n" };

const SUPERVISOR = { id: "11111111-1111-1111-1111-111111111111", name: "scratch-box", remote_controls: [] };

beforeEach(() => {
  saved.mockReset();
  // By default no supervisor can run one: the by-hand way is the only one.
  vi.spyOn(apiClient, "listOwnRemoteControlSupervisors").mockResolvedValue({ provisionFromBoard: false, supervisors: [] });
});

function ownSupervisors(supervisors: OwnRemoteControlSupervisor[]) {
  return vi.spyOn(apiClient, "listOwnRemoteControlSupervisors").mockResolvedValue({ provisionFromBoard: true, supervisors });
}

function entry(name: string, observed: DesiredAgentView["observed"], generation = 1): DesiredAgentView {
  return { id: `id-${name}`, kind: "remote_control", name, harness: "claude", state: "running", launch: "supervisor", generation, resume_generation: 0, observed };
}

async function register(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.type(screen.getByLabelText("Remote control name"), name);
  await user.click(screen.getByRole("button", { name: "Register remote control" }));
}

describe("RemoteControlSetup", () => {
  it("is closed until asked for, like Add a machine's Add a slot manually", async () => {
    const { container } = await renderSignedIn(<RemoteControlSetup />);

    const section = container.querySelector("details.remote-control-setup") as HTMLDetailsElement;
    expect(section).not.toHaveAttribute("open");
    expect(within(section).getByText("Add a remote control", { selector: "summary" })).toBeInTheDocument();
  });

  it("only adds: it neither loads nor manages existing remote controls", async () => {
    const list = vi.spyOn(apiClient, "listConductors");

    await renderSignedIn(<RemoteControlSetup asView />);

    expect(list).not.toHaveBeenCalled();
    for (const name of [/Disable/, /Enable/, /Archive/, /Remove/, /New setup script/, /Edit config/]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("registers a remote control, saves its script, tells the page and shows the setup command once", async () => {
    const user = userEvent.setup();
    const registerApi = vi.spyOn(apiClient, "registerConductor").mockResolvedValue(SCRIPT);
    const onChanged = vi.fn();

    await renderSignedIn(<RemoteControlSetup onChanged={onChanged} />);
    await register(user, " mine ");

    expect(registerApi).toHaveBeenCalledWith("mine");
    expect(saved).toHaveBeenCalledWith(SCRIPT.filename, SCRIPT.script);
    expect(onChanged).toHaveBeenCalledTimes(1);
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("bash slop-haus-setup-mine.sh");
    expect(status).toHaveTextContent("only time");

    await user.click(within(status).getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    // The script itself is never put on the page.
    expect(document.body.textContent).not.toContain("#!/bin/sh");
  });

  it("asks for a name before calling the server", async () => {
    const user = userEvent.setup();
    const registerApi = vi.spyOn(apiClient, "registerConductor");

    await renderSignedIn(<RemoteControlSetup />);
    await user.click(screen.getByRole("button", { name: "Register remote control" }));

    expect(registerApi).not.toHaveBeenCalled();
    expect(await screen.findByRole("alert")).toHaveTextContent("name");
  });

  it("surfaces the server's refusal", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "registerConductor").mockRejectedValue(new ApiError(422, "agent: name already in use"));

    const onChanged = vi.fn();

    await renderSignedIn(<RemoteControlSetup onChanged={onChanged} />);
    await register(user, "mine");

    expect(await screen.findByRole("alert")).toHaveTextContent("name already in use");
    expect(saved).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("asks the person to confirm it's them when the sign-in is stale, then registers", async () => {
    const user = userEvent.setup();
    const registerApi = vi
      .spyOn(apiClient, "registerConductor")
      .mockRejectedValueOnce(new ApiError(403, "confirm it's you", "reauth_required"))
      .mockResolvedValueOnce(SCRIPT);
    vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
      password: { email: "person@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    });
    vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);

    await renderSignedIn(<RemoteControlSetup />);
    await register(user, "mine");

    expect(await screen.findByText("Confirm it’s you")).toBeInTheDocument();
    expect(saved).not.toHaveBeenCalled();
    await user.type(screen.getByLabelText("Current password"), "correct-password");
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await vi.waitFor(() => expect(registerApi).toHaveBeenCalledTimes(2));
    expect(registerApi).toHaveBeenNthCalledWith(1, "mine");
    expect(registerApi).toHaveBeenNthCalledWith(2, "mine");
    expect(saved).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Confirm it’s you")).not.toBeInTheDocument();
  });

  describe("the step-up prompt's return address", () => {
    async function stepUpThenProvider(asView: boolean) {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "registerConductor").mockRejectedValue(new ApiError(403, "confirm it's you", "reauth_required"));
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
        identities: [{ provider: "dex", subject: "1", login: "person@example.com", usable: true }],
        password_login_enabled: false,
        reauth_required_after: "2026-01-01T00:00:00Z",
      });
      const start = vi.spyOn(apiClient, "startReauth").mockResolvedValue("https://dex.example/reauth");
      vi.spyOn(navigation, "redirectBrowserTo").mockImplementation(() => {});

      await renderSignedIn(<RemoteControlSetup asView={asView} />, {
        providers: [{ name: "dex", display_name: "dex" }],
      });
      await register(user, "mine");
      await user.click(await screen.findByRole("button", { name: "Continue with dex" }));
      return start;
    }

    it("is the setup view when it is a view, so a step-up lands back on the form", async () => {
      const start = await stepUpThenProvider(true);

      expect(start).toHaveBeenCalledWith("dex", "/remote-control?view=setup");
    });

    it("is the plain page as a section on a phone", async () => {
      const start = await stepUpThenProvider(false);

      expect(start).toHaveBeenCalledWith("dex", "/remote-control");
    });
  });

  it("says the script is offered once, in the view where leaving it loses it", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "registerConductor").mockResolvedValue(SCRIPT);

    await renderSignedIn(<RemoteControlSetup asView />);
    await register(user, "mine");

    expect(await screen.findByRole("status")).toHaveTextContent("this is the only time it is offered");
  });

  describe("on a supervisor the person signed in", () => {
    it("adds it on the one machine there is, without asking which, and the page follows", async () => {
      const user = userEvent.setup();
      const list = ownSupervisors([SUPERVISOR]);
      const add = vi.spyOn(apiClient, "addOwnRemoteControl").mockResolvedValue(entry("mine", { generation: 0 }));
      const onChanged = vi.fn();

      await renderSignedIn(<RemoteControlSetup asView onChanged={onChanged} />);
      const form = await screen.findByRole("form", { name: "Add a remote control on a machine" });
      expect(form).toHaveTextContent("On scratch-box");
      expect(within(form).queryByLabelText("Machine")).not.toBeInTheDocument();
      await user.type(within(form).getByLabelText("Remote control name"), " mine ");
      await user.click(within(form).getByRole("button", { name: "Add remote control" }));

      await vi.waitFor(() => expect(add).toHaveBeenCalledWith(SUPERVISOR.id, "mine", undefined));
      expect(onChanged).toHaveBeenCalledTimes(1);
      await vi.waitFor(() => expect(list.mock.calls.length).toBeGreaterThan(1));
      expect(saved).not.toHaveBeenCalled();
    });

    it("asks which machine when there is more than one", async () => {
      const user = userEvent.setup();
      ownSupervisors([SUPERVISOR, { ...SUPERVISOR, id: "22222222-2222-2222-2222-222222222222", name: "other-box" }]);
      const add = vi.spyOn(apiClient, "addOwnRemoteControl").mockResolvedValue(entry("mine", { generation: 0 }));

      await renderSignedIn(<RemoteControlSetup asView />);
      const form = await screen.findByRole("form", { name: "Add a remote control on a machine" });
      await user.click(within(form).getByLabelText("Machine"));
      await user.click(screen.getByRole("option", { name: "other-box" }));
      await user.type(within(form).getByLabelText("Remote control name"), "mine");
      await user.click(within(form).getByRole("button", { name: "Add remote control" }));

      await vi.waitFor(() => expect(add).toHaveBeenCalledWith("22222222-2222-2222-2222-222222222222", "mine", undefined));
    });

    it("shows how each one it runs is coming along, the supervisor's reason first-hand", async () => {
      ownSupervisors([
        {
          ...SUPERVISOR,
          remote_controls: [
            entry("waiting-one", { generation: 0 }),
            entry("broken-one", { generation: 1, phase: "failed", reason: "fatal: could not read Username" }),
            { ...entry("gone-one", { generation: 1, phase: "running" }), state: "removed" },
          ],
        },
      ]);

      await renderSignedIn(<RemoteControlSetup asView />);

      const list = await screen.findByRole("list", { name: "Remote controls your supervisors run" });
      expect(list).toHaveTextContent("waiting-one on scratch-box: Waiting for supervisor");
      expect(list).toHaveTextContent("broken-one on scratch-box: Failed: fatal: could not read Username");
      expect(list).not.toHaveTextContent("gone-one");
    });

    it("asks the person to confirm it's them when the sign-in is stale, then adds", async () => {
      const user = userEvent.setup();
      ownSupervisors([SUPERVISOR]);
      const add = vi
        .spyOn(apiClient, "addOwnRemoteControl")
        .mockRejectedValueOnce(new ApiError(403, "confirm it's you", "reauth_required"))
        .mockResolvedValueOnce(entry("mine", { generation: 0 }));
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
        password: { email: "person@example.com", verified: true },
        identities: [],
        password_login_enabled: true,
        reauth_required_after: "2026-01-01T00:00:00Z",
      });
      vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);

      await renderSignedIn(<RemoteControlSetup asView />);
      const form = await screen.findByRole("form", { name: "Add a remote control on a machine" });
      await user.type(within(form).getByLabelText("Remote control name"), "mine");
      await user.click(within(form).getByRole("button", { name: "Add remote control" }));
      expect(await screen.findByText("Confirm it’s you")).toBeInTheDocument();
      await user.type(screen.getByLabelText("Current password"), "correct-password");
      await user.click(screen.getByRole("button", { name: "Confirm" }));

      await vi.waitFor(() => expect(add).toHaveBeenCalledTimes(2));
      expect(add).toHaveBeenNthCalledWith(2, SUPERVISOR.id, "mine", undefined);
    });

    it("keeps running one by hand closed as the fallback", async () => {
      ownSupervisors([SUPERVISOR]);

      const { container } = await renderSignedIn(<RemoteControlSetup asView />);
      await screen.findByRole("form", { name: "Add a remote control on a machine" });

      expect(container.querySelector("details.remote-control-setup__by-hand")).not.toHaveAttribute("open");
    });

    it("with no supervisor of theirs, says where to set one up and opens running one by hand", async () => {
      ownSupervisors([]);

      const { container } = await renderSignedIn(<RemoteControlSetup asView />);

      expect(await screen.findByRole("link", { name: "the Workers page" })).toHaveAttribute("href", "/workers");
      expect(screen.queryByRole("form", { name: "Add a remote control on a machine" })).not.toBeInTheDocument();
      await vi.waitFor(() => expect(container.querySelector("details.remote-control-setup__by-hand")).toHaveAttribute("open"));
    });

    it("with the board not provisioning, says so rather than offer a machine", async () => {
      await renderSignedIn(<RemoteControlSetup asView />);

      expect(await screen.findByRole("note")).toHaveTextContent("does not provision agents from the board");
      expect(screen.queryByRole("form", { name: "Add a remote control on a machine" })).not.toBeInTheDocument();
    });
  });
});
