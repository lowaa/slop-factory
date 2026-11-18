import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeAgent, makeConductor, makePendingDeviceAuthorization } from "@/test/fixtures";
import { selectedValueOf } from "@/test/widgets";
import { WithAuthPagesConfig } from "@/test/with_auth_pages";
import DevicePage from "./device_page";
import { clearPendingDevice, hasPendingDeviceVisit } from "./pending_device";

describe("DevicePage", () => {
  beforeEach(() => {
    clearPendingDevice();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows a blank code form when there is no ?code in the URL", () => {
    window.history.replaceState({}, "", "/device");

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(screen.getByLabelText("Device code")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Look up" })).toBeInTheDocument();
  });

  it("prefills the code from ?code and looks it up automatically", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    const lookup = vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(screen.getByLabelText("Device code")).toHaveValue("BCDF-GHJK");
    await vi.waitFor(() => expect(lookup).toHaveBeenCalledWith("BCDF-GHJK"));
    expect(await screen.findByText(/wants to sign in as one of your agents/)).toBeInTheDocument();
  });

  it("looks up a manually typed code on submit", async () => {
    window.history.replaceState({}, "", "/device");
    const lookup = vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await user.type(screen.getByLabelText("Device code"), "bcdf-ghjk");
    await user.click(screen.getByRole("button", { name: "Look up" }));

    expect(lookup).toHaveBeenCalledWith("bcdf-ghjk");
    expect(await screen.findByText(pending().client_name, { exact: false })).toBeInTheDocument();
  });

  it("shows the server's 404 for an unknown or expired code", async () => {
    window.history.replaceState({}, "", "/device?code=DEAD-BEEF");
    vi.spyOn(apiClient, "lookupDevice").mockRejectedValue(new ApiError(404, "no such pending authorization; it may have expired or been decided already"));

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(/expired or been decided already/);
  });

  // As on the consent page: a failed lookup must not leave the stash that
  // routes the app back here behind.
  it("lets go of the pending visit once shown, even when the code is unknown", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockRejectedValue(new ApiError(404, "unknown"));

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    await screen.findByRole("alert");
    expect(hasPendingDeviceVisit()).toBe(false);
  });

  it("shows the server's 429 after too many tries", async () => {
    window.history.replaceState({}, "", "/device?code=DEAD-BEEF");
    vi.spyOn(apiClient, "lookupDevice").mockRejectedValue(new ApiError(429, "oauth: too many device code attempts"));

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(/too many/i);
  });

  it("preselects the agent named in ?agent when it exists and is enabled", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK&agent=builder-x");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: "a1", name: "builder-x", disabled_at: null }),
      makeAgent({ id: "a2", name: "other", disabled_at: null }),
    ]);

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    await screen.findByRole("combobox", { name: "Existing agent" });
    expect(selectedValueOf("Existing agent")).toBe("builder-x");
    expect(screen.getByLabelText("An existing agent")).toBeChecked();
  });

  // A conductor's setup script ends on this page: its owner picks it here.
  it("preselects the person's own conductor named in ?agent", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK&agent=my-conductor");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({
      own: [makeConductor({ id: "c1", name: "my-conductor" })],
      others: [makeConductor({ id: "c2", name: "theirs", owned_by_me: false })],
    });

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    const select = await screen.findByRole("combobox", { name: "Existing agent" });
    expect(select).toHaveTextContent("my-conductor");
    await userEvent.setup().click(select);
    expect(screen.getByRole("option", { name: "my-conductor" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "theirs" })).not.toBeInTheDocument();
  });

  it("prefills the new-agent name from ?agent when no agent by that name exists", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK&agent=builder-x");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a2", name: "other", disabled_at: null })]);

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await screen.findByText(/wants to sign in as one of your agents/);

    expect(screen.getByLabelText("An existing agent")).not.toBeChecked();
    expect(screen.getByLabelText("New agent name")).toHaveValue("builder-x");
  });

  it("approves with the preselected existing agent and shows the done state", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK&agent=builder-x");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization({ user_code: "BCDF-GHJK" }));
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-x", disabled_at: null })]);
    const decide = vi.spyOn(apiClient, "decideDevice").mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await screen.findByRole("combobox", { name: "Existing agent" });
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("BCDF-GHJK", { approve: true, agent_id: "a1" });
    expect(await screen.findByText(/Done — return to your terminal/)).toBeInTheDocument();
  });

  it("approves with a typed new agent name", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization({ user_code: "BCDF-GHJK" }));
    const decide = vi.spyOn(apiClient, "decideDevice").mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await user.type(await screen.findByLabelText("New agent name"), "builder-y");
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("BCDF-GHJK", { approve: true, agent_name: "builder-y", agent_kind: "worker" });
    expect(await screen.findByText(/Done — return to your terminal/)).toBeInTheDocument();
  });

  describe("supervisor hint (?kind=supervisor&slots=N)", () => {
    function open(query: string) {
      window.history.replaceState({}, "", `/device?code=BCDF-GHJK${query}`);
      vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization({ user_code: "BCDF-GHJK" }));
      return vi.spyOn(apiClient, "decideDevice").mockResolvedValue(undefined);
    }
    function renderPage() {
      render(
        <WithAuthPagesConfig>
          <DevicePage />
        </WithAuthPagesConfig>,
      );
    }

    it("shows a supervisor with N slots and approves exactly that", async () => {
      const decide = open("&kind=supervisor&slots=6&agent=sup-1");
      const user = userEvent.setup();

      renderPage();
      expect(await screen.findByText(/up to 6 workers at once/)).toBeInTheDocument();
      expect(screen.getByLabelText("A supervisor")).toBeChecked();
      expect(screen.getByLabelText("Workers it may run at once")).toHaveValue(6);
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("BCDF-GHJK", {
        approve: true, agent_name: "sup-1", agent_kind: "supervisor", slot_cap: 6,
      });
    });

    it("grants what the form says after the human changes the count", async () => {
      const decide = open("&kind=supervisor&slots=6&agent=sup-1");
      const user = userEvent.setup();

      renderPage();
      const slots = await screen.findByLabelText("Workers it may run at once");
      await user.clear(slots);
      await user.type(slots, "2");
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("BCDF-GHJK", expect.objectContaining({ agent_kind: "supervisor", slot_cap: 2 }));
    });

    it("does not preselect an existing agent that shares the requested name", async () => {
      open("&kind=supervisor&slots=3&agent=builder-x");
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-x", disabled_at: null })]);

      renderPage();
      await screen.findByLabelText("A supervisor");

      expect(screen.getByLabelText("An existing agent")).not.toBeChecked();
    });

    it.each([
      ["slots above the server limit", "&kind=supervisor&slots=17"],
      ["slots of zero", "&kind=supervisor&slots=0"],
      ["non-numeric slots", "&kind=supervisor&slots=abc"],
      ["fractional slots", "&kind=supervisor&slots=2.5"],
      ["slots without kind", "&slots=4"],
      ["an unknown kind", "&kind=admin&slots=4"],
      ["a supervisor without slots", "&kind=supervisor"],
    ])("ignores %s and stays on a worker", async (_name, query) => {
      const decide = open(query);
      const user = userEvent.setup();

      renderPage();
      await user.type(await screen.findByLabelText("New agent name"), "w");
      expect(screen.queryByText(/asked to be authorized as a supervisor/)).not.toBeInTheDocument();
      expect(screen.getByLabelText("A worker")).toBeChecked();
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("BCDF-GHJK", { approve: true, agent_name: "w", agent_kind: "worker" });
    });

    it("sends a supervisor when the human chooses one by hand, with no hint", async () => {
      const decide = open("");
      const user = userEvent.setup();

      renderPage();
      await user.type(await screen.findByLabelText("New agent name"), "sup-2");
      await user.click(screen.getByLabelText("A supervisor"));
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("BCDF-GHJK", {
        approve: true, agent_name: "sup-2", agent_kind: "supervisor", slot_cap: 4,
      });
    });

    it("sends no slot_cap when the human picks an existing agent over the hint", async () => {
      const decide = open("&kind=supervisor&slots=6");
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-x", disabled_at: null })]);
      const user = userEvent.setup();

      renderPage();
      await user.click(await screen.findByLabelText("An existing agent"));
      await user.click(await screen.findByRole("combobox", { name: "Existing agent" }));
      await user.click(await screen.findByRole("option", { name: "builder-x" }));
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("BCDF-GHJK", { approve: true, agent_id: "a1" });
    });
  });

  it("denies the device authorization and shows the done state", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization({ user_code: "BCDF-GHJK" }));
    const decide = vi.spyOn(apiClient, "decideDevice").mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await user.click(await screen.findByRole("button", { name: "Deny" }));

    expect(decide).toHaveBeenCalledWith("BCDF-GHJK", { approve: false });
    expect(await screen.findByText(/Denied\. Done — return to your terminal/)).toBeInTheDocument();
  });

  it("surfaces a name-already-taken conflict without leaving the agent picker", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization({ user_code: "BCDF-GHJK" }));
    vi.spyOn(apiClient, "decideDevice").mockRejectedValue(new ApiError(409, "oauth: an agent with that name already exists"));
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );
    await user.type(await screen.findByLabelText("New agent name"), "builder-x");
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("already exists");
    expect(screen.getByLabelText("New agent name")).toBeInTheDocument();
  });

  it("says where and when the sign-in was started", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(makePendingDeviceAuthorization());

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByText("100.77.108.4")).toBeInTheDocument();
    expect(document.querySelector("time")).toHaveAttribute("datetime", "2026-01-01T00:05:00Z");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("doesn't warn when where it was started is unknown", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(
      makePendingDeviceAuthorization({ started_from: "", from_this_network: false }),
    );

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByText(/Only approve a code you started yourself/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // RFC 8628's remote phishing: someone else started it and sent the code.
  it("warns when the sign-in was started from another network", async () => {
    window.history.replaceState({}, "", "/device?code=BCDF-GHJK");
    vi.spyOn(apiClient, "lookupDevice").mockResolvedValue(
      makePendingDeviceAuthorization({ started_from: "203.0.113.9", from_this_network: false }),
    );

    render(
      <WithAuthPagesConfig>
        <DevicePage />
      </WithAuthPagesConfig>,
    );

    const warning = await screen.findByRole("alert");
    expect(warning).toHaveTextContent("203.0.113.9");
    expect(warning.querySelector("time")).toHaveAttribute("datetime", "2026-01-01T00:05:00Z");
    expect(warning).toHaveTextContent(/isn’t this browser’s network/);
    expect(warning).toHaveTextContent(/deny it/);
  });
});

function pending() {
  return makePendingDeviceAuthorization();
}
