import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { makeAgent, makePendingAuthorization } from "@/test/fixtures";
import { chooseFromDropdown } from "@/test/widgets";
import { WithAuthPagesConfig } from "@/test/with_auth_pages";
import AuthorizePage from "./authorize_page";
import { clearPendingAuthorizeRequest, hasPendingAuthorizeRequest } from "./pending_authorize";

describe("AuthorizePage", () => {
  let redirectSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // pending_authorize.ts's id is module state (see its own doc comment)
    // so it survives between test cases here unless cleared explicitly.
    clearPendingAuthorizeRequest();
    redirectSpy = vi.spyOn(navigation, "redirectBrowserTo").mockImplementation(() => {});
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows a missing-request error when the URL has no request id", async () => {
    window.history.replaceState({}, "", "/authorize");

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("This authorization link is missing its request id.");
  });

  it("shows the loading state before the pending request resolves", () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockReturnValue(new Promise(() => {}));

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );

    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });

  it("shows the server's 404 when the request has expired or been decided already", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockRejectedValue(
      new ApiError(404, "no such pending authorization; it may have expired or been decided already"),
    );

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(/expired or been decided already/);
  });

  // An expired request is a dead end, and must not become a trap: once the
  // page has read the id, the stash that routes the app back here goes.
  it("lets go of the pending request once shown, even when it has expired", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockRejectedValue(new ApiError(404, "expired"));

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );

    await screen.findByRole("alert");
    expect(hasPendingAuthorizeRequest()).toBe(false);
  });

  it("shows the client name, redirect host and reads the request id from the query string", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    const getAuthRequest = vi
      .spyOn(apiClient, "getAuthRequest")
      .mockResolvedValue(makePendingAuthorization({ client_name: "Claude Code", redirect_host: "127.0.0.1:9876" }));

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );

    expect(await screen.findByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText(/127\.0\.0\.1:9876/)).toBeInTheDocument();
    expect(getAuthRequest).toHaveBeenCalledWith("req-1");
  });

  it("approves with a new agent's name", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    const decide = vi
      .spyOn(apiClient, "decideAuthRequest")
      .mockResolvedValue("http://127.0.0.1:9876/callback?code=abc&state=xyz");
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.type(await screen.findByLabelText("New agent name"), "builder-x");
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("req-1", { approve: true, agent_name: "builder-x", agent_kind: "worker" });
    // Negative-run: comment out the redirectBrowserTo call in
    // authorize_page.tsx's decide() -- this assertion is what catches it,
    // since decide() being called at all does not prove the browser was
    // ever sent anywhere.
    await vi.waitFor(() => expect(redirectSpy).toHaveBeenCalledWith("http://127.0.0.1:9876/callback?code=abc&state=xyz"));
  });

  it("approves as an existing enabled agent, and never navigates before that decision returns", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: "a1", name: "builder-1", disabled_at: null }),
      makeAgent({ id: "a2", name: "retired", disabled_at: "2026-01-01T00:00:00Z" }),
    ]);
    let resolveDecision!: (redirectTo: string) => void;
    const decide = vi.spyOn(apiClient, "decideAuthRequest").mockReturnValue(
      new Promise((resolve) => {
        resolveDecision = resolve;
      }),
    );
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.click(await screen.findByLabelText("An existing agent"));
    const select = await screen.findByRole("combobox", { name: "Existing agent" });
    // Disabled agents are never offered -- the option simply doesn't exist.
    await user.click(select);
    expect(screen.queryByRole("option", { name: "retired" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "builder-1" }));
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("req-1", { approve: true, agent_id: "a1" });
    expect(redirectSpy).not.toHaveBeenCalled();

    resolveDecision("http://127.0.0.1:9876/callback?code=abc");
    await vi.waitFor(() => expect(redirectSpy).toHaveBeenCalledWith("http://127.0.0.1:9876/callback?code=abc"));
  });

  it("disables Approve until an existing agent is picked or a new name is typed", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await screen.findByText(/wants to act as one of your agents/);
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();

    await user.click(screen.getByLabelText("An existing agent"));
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    await chooseFromDropdown(user, "Existing agent", "builder-1");
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  describe("replacing an existing agent", () => {
    const OFF = "2026-01-01T00:00:00Z";

    function renderWithAgents() {
      window.history.replaceState({}, "", "/authorize?request=req-1");
      vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "builder-1" }),
        makeAgent({ id: "a2", name: "switched-off", disabled_at: OFF }),
        makeAgent({ id: "a3", name: "put-away", disabled_at: OFF, archived_at: OFF }),
      ]);
      vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
      render(
        <WithAuthPagesConfig>
          <AuthorizePage />
        </WithAuthPagesConfig>,
      );
    }

    it("sends the agent to replace with the new agent, offering disabled agents but not archived ones", async () => {
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderWithAgents();

      await user.type(await screen.findByLabelText("New agent name"), "my-console");
      await user.click(screen.getByRole("checkbox", { name: "Replace an existing agent" }));
      await user.click(await screen.findByRole("combobox", { name: "Agent to replace" }));
      expect(screen.getByRole("option", { name: "switched-off" })).toBeInTheDocument();
      expect(screen.queryByRole("option", { name: "put-away" })).not.toBeInTheDocument();
      await user.click(screen.getByRole("option", { name: "builder-1" }));
      await user.click(screen.getByLabelText("My interactive session (console)"));
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("req-1", {
        approve: true,
        agent_name: "my-console",
        agent_kind: "console",
        replaces_agent_id: "a1",
      });
    });

    it("holds Approve until the agent to replace is picked, and sends nothing extra when not replacing", async () => {
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderWithAgents();

      await user.type(await screen.findByLabelText("New agent name"), "fresh");
      await user.click(screen.getByRole("checkbox", { name: "Replace an existing agent" }));
      expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
      await chooseFromDropdown(user, "Agent to replace", "builder-1");
      expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();

      await user.click(screen.getByRole("checkbox", { name: "Replace an existing agent" }));
      await user.click(screen.getByRole("button", { name: "Approve" }));
      expect(decide).toHaveBeenCalledWith("req-1", { approve: true, agent_name: "fresh", agent_kind: "worker" });
    });

    it("is not offered when signing in as an existing agent", async () => {
      const user = userEvent.setup();
      renderWithAgents();

      await user.click(await screen.findByLabelText("An existing agent"));

      expect(screen.queryByRole("checkbox", { name: "Replace an existing agent" })).not.toBeInTheDocument();
    });
  });

  it("denies the request and follows the server's redirect", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    const decide = vi
      .spyOn(apiClient, "decideAuthRequest")
      .mockResolvedValue("http://127.0.0.1:9876/callback?error=access_denied");
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.click(await screen.findByRole("button", { name: "Deny" }));

    expect(decide).toHaveBeenCalledWith("req-1", { approve: false });
    await vi.waitFor(() =>
      expect(redirectSpy).toHaveBeenCalledWith("http://127.0.0.1:9876/callback?error=access_denied"),
    );
  });

  it("surfaces a name-already-taken conflict without navigating away", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    vi.spyOn(apiClient, "decideAuthRequest").mockRejectedValue(
      new ApiError(409, "oauth: an agent with that name already exists"),
    );
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.type(await screen.findByLabelText("New agent name"), "builder-x");
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("already exists");
    expect(redirectSpy).not.toHaveBeenCalled();
  });

  it("clears the pending request id once a decision redirects the browser away", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/callback?error=access_denied");
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.click(await screen.findByRole("button", { name: "Deny" }));

    await vi.waitFor(() => expect(hasPendingAuthorizeRequest()).toBe(false));
  });

  it("sends the console kind, and explains it, when the interactive session is chosen", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    const decide = vi
      .spyOn(apiClient, "decideAuthRequest")
      .mockResolvedValue("http://127.0.0.1:9876/callback?code=abc");
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.type(await screen.findByLabelText("New agent name"), "my-session");
    expect(screen.getByLabelText("A worker")).toBeChecked();
    expect(screen.getByText(/takes work from the queue/)).toBeInTheDocument();
    expect(screen.queryByText(/never waits in the queue/)).not.toBeInTheDocument();

    await user.click(screen.getByLabelText("My interactive session (console)"));
    expect(screen.getByText(/owner-only actions/)).toBeInTheDocument();
    expect(screen.getByText(/never waits in the queue/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("req-1", {
      approve: true,
      agent_name: "my-session",
      agent_kind: "console",
    });
  });

  describe("supervisor choice", () => {
    function renderNew() {
      window.history.replaceState({}, "", "/authorize?request=req-1");
      vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
      render(
        <WithAuthPagesConfig>
          <AuthorizePage />
        </WithAuthPagesConfig>,
      );
    }
    const CAP = "Workers it may run at once";

    it("sends the supervisor kind and the slot cap, and states what the consent grants", async () => {
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderNew();

      await user.type(await screen.findByLabelText("New agent name"), "box-sup");
      await user.click(screen.getByLabelText("A supervisor"));
      expect(screen.getByText(/without asking you again/)).toBeInTheDocument();
      expect(screen.getByLabelText(CAP)).toHaveValue(4);
      await user.clear(screen.getByLabelText(CAP));
      await user.type(screen.getByLabelText(CAP), "7");
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("req-1", {
        approve: true,
        agent_name: "box-sup",
        agent_kind: "supervisor",
        slot_cap: 7,
      });
    });

    it("defaults to worker and shows no cap input for worker or console, sending no cap", async () => {
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderNew();

      await user.type(await screen.findByLabelText("New agent name"), "w");
      expect(screen.getByLabelText("A worker")).toBeChecked();
      expect(screen.queryByLabelText(CAP)).not.toBeInTheDocument();
      await user.click(screen.getByLabelText("My interactive session (console)"));
      expect(screen.queryByLabelText(CAP)).not.toBeInTheDocument();
      // A cap typed for the supervisor must not leak into another kind.
      await user.click(screen.getByLabelText("A supervisor"));
      await user.clear(screen.getByLabelText(CAP));
      await user.type(screen.getByLabelText(CAP), "9");
      await user.click(screen.getByLabelText("A worker"));
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("req-1", { approve: true, agent_name: "w", agent_kind: "worker" });
    });

    it.each([1, 16])("accepts the boundary cap %i", async (cap) => {
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderNew();

      await user.type(await screen.findByLabelText("New agent name"), "box-sup");
      await user.click(screen.getByLabelText("A supervisor"));
      await user.clear(screen.getByLabelText(CAP));
      await user.type(screen.getByLabelText(CAP), String(cap));
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("req-1", expect.objectContaining({ slot_cap: cap }));
    });

    it.each(["0", "17", "", "2.5"])("blocks Approve for the out-of-range cap %j", async (value) => {
      const user = userEvent.setup();
      renderNew();

      await user.type(await screen.findByLabelText("New agent name"), "box-sup");
      await user.click(screen.getByLabelText("A supervisor"));
      await user.clear(screen.getByLabelText(CAP));
      if (value) await user.type(screen.getByLabelText(CAP), value);

      expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    });

    it("offers no replace-an-agent option for a supervisor, and sends none", async () => {
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a-1", name: "builder-1" })]);
      vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
      const decide = vi.spyOn(apiClient, "decideAuthRequest").mockResolvedValue("http://127.0.0.1:9876/cb?code=abc");
      const user = userEvent.setup();
      renderNew();

      await user.type(await screen.findByLabelText("New agent name"), "box-sup");
      await user.click(await screen.findByRole("checkbox", { name: "Replace an existing agent" }));
      await user.click(screen.getByLabelText("A supervisor"));
      expect(screen.queryByRole("checkbox", { name: "Replace an existing agent" })).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Approve" }));

      expect(decide).toHaveBeenCalledWith("req-1", {
        approve: true,
        agent_name: "box-sup",
        agent_kind: "supervisor",
        slot_cap: 4,
      });
    });
  });

  it("offers no kind choice and sends none when an existing agent is picked", async () => {
    window.history.replaceState({}, "", "/authorize?request=req-1");
    vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue(makePendingAuthorization());
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
    const decide = vi
      .spyOn(apiClient, "decideAuthRequest")
      .mockResolvedValue("http://127.0.0.1:9876/callback?code=abc");
    const user = userEvent.setup();

    render(
      <WithAuthPagesConfig>
        <AuthorizePage />
      </WithAuthPagesConfig>,
    );
    await user.click(await screen.findByLabelText("My interactive session (console)"));
    await user.click(screen.getByLabelText("An existing agent"));
    expect(screen.queryByLabelText("My interactive session (console)")).not.toBeInTheDocument();
    await chooseFromDropdown(user, "Existing agent", "builder-1");
    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decide).toHaveBeenCalledWith("req-1", {
      approve: true,
      agent_id: "a1",
    });
  });
});
