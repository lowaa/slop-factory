import {} from "node:child_process";
import {} from "node:os";
import {} from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { apiClient, ApiError} from "@/api";
import {
  makeAgent,
  makeVariantVersion,
  makeHarnessConfig,
  makeHarnessConfigScope,
  } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import AgentsPanel from "../agents_panel";
import { stubPanelDefaults, stubSideLists } from "../agents_panel_test_support";

// The setup help's harness tab is also called "Claude Code".

stubPanelDefaults();

describe("AgentsPanel variants", () => {
  const AGENT = "11111111-1111-1111-1111-111111111111";
  const FAST = makeVariantVersion();

  beforeEach(() => stubSideLists());

  it("lets an owner put an agent on a variant, then refetches the roster and config", async () => {
    vi.spyOn(apiClient, "listVariants").mockResolvedValue([FAST]);
    const list = vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    const config = vi.spyOn(apiClient, "getHarnessConfig");
    const set = vi.spyOn(apiClient, "setAgentVariant").mockResolvedValue();
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Variant" }));
    await chooseFromDropdown(user, "Variant", /^fast v1/);
    const calls = list.mock.calls.length;
    const configCalls = config.mock.calls.length;
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith(AGENT, FAST.id);
    await waitFor(() => expect(list.mock.calls.length).toBeGreaterThan(calls));
    await waitFor(() => expect(config.mock.calls.length).toBeGreaterThan(configCalls));
  });

  it("keeps an archived version the agent is on as its current choice", async () => {
    vi.spyOn(apiClient, "listVariants").mockResolvedValue([FAST]);
    const archived = { name: "old", version: 4, harness: "pi", version_id: "archived-id" };
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1", variant: archived })]);
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Variant" }));

    expect(selectedValueOf("Variant")).toBe("old v4 (pi, archived)");
  });

  it("takes an agent off its variant with a null version", async () => {
    vi.spyOn(apiClient, "listVariants").mockResolvedValue([FAST]);
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: AGENT, name: "builder-1", variant: { name: "fast", version: 1, harness: "claude", version_id: FAST.id } }),
    ]);
    const set = vi.spyOn(apiClient, "setAgentVariant").mockResolvedValue();
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Variant" }));
    await chooseFromDropdown(user, "Variant", "No variant");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(set).toHaveBeenCalledWith(AGENT, null));
  });

  it("shows the server's refusal and keeps the picker open", async () => {
    vi.spyOn(apiClient, "listVariants").mockResolvedValue([FAST]);
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    vi.spyOn(apiClient, "setAgentVariant").mockRejectedValue(new ApiError(422, "that version is archived"));
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Variant" }));
    await chooseFromDropdown(user, "Variant", /^fast v1/);
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("that version is archived");
    expect(screen.getByRole("combobox", { name: "Variant" })).toBeInTheDocument();
  });

  it("offers a member no picker, but still names the agent's variant", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ name: "builder-1", variant: { name: "fast", version: 1, harness: "claude", version_id: FAST.id } }),
    ]);
    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText("variant fast v1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Variant" })).not.toBeInTheDocument();
  });

  it("shows an override as inactive, with its values, while the agent is in a variant", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        id: AGENT,
        name: "builder-1",
        variant: { name: "fast", version: 1, harness: "claude", version_id: FAST.id },
        harness_override_inactive: true,
      }),
    ]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({ overrides: [makeHarnessConfigScope({ agent_id: AGENT, settings: { claude: { effort: "max" } } })] }),
    );
    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText("config override (inactive)")).toBeInTheDocument();
    const note = screen.getByRole("note");
    expect(note).toHaveTextContent("fast v1");
    expect(note).toHaveTextContent("applies again when the agent leaves the variant");
    expect(note).toHaveTextContent("Effort max");
  });

  it("leaves an active override unmarked", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({ overrides: [makeHarnessConfigScope({ agent_id: AGENT, settings: { claude: { effort: "max" } } })] }),
    );
    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText("config override")).toBeInTheDocument();
    expect(screen.queryByText(/inactive/)).not.toBeInTheDocument();
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });
});
