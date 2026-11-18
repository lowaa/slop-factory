import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type FleetInstructionsVersion } from "@/api";
import { makeHarnessConfig, makeVariantVersion } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import VariantsWidget from "./variants";

afterEach(() => vi.restoreAllMocks());

const FLEET_ID = "bbbbbbbb-0000-0000-0000-000000000001";
const FLEET: FleetInstructionsVersion = {
  id: FLEET_ID,
  name: "house",
  version: 3,
  kind: "fleet",
  body: "Be brief.",
  created_by: null,
  created_at: "2026-10-04T12:00:00Z",
  archived: false,
  archived_at: null,
};
const ARCHIVED_FLEET = { ...FLEET, id: "bbbbbbbb-0000-0000-0000-000000000002", name: "old", version: 1, archived: true };

const V1 = makeVariantVersion({ id: "v1", version: 1, settings: { model: "haiku" } });
const V2 = makeVariantVersion({
  id: "v2",
  version: 2,
  settings: { model: "sonnet", effort: "high" },
  fleet_instructions_version_id: FLEET_ID,
});

beforeEach(() => {
  vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
  vi.spyOn(apiClient, "getFleetInstructions").mockResolvedValue({
    versions: [FLEET, ARCHIVED_FLEET],
    default_id: FLEET_ID,
    can_edit: true,
    max_body_bytes: 100,
  });
});

function mockList(versions = [V2, V1]) {
  return vi.spyOn(apiClient, "listVariants").mockResolvedValue(versions);
}

describe("VariantsWidget", () => {
  it("lists each version with its harness, settings and pinned fleet instructions", async () => {
    mockList();
    await renderSignedIn(<VariantsWidget />);

    const items = await screen.findAllByRole("listitem");
    expect(items[0]).toHaveTextContent("fast v2");
    expect(items[0]).toHaveTextContent("Claude Code");
    expect(items[0]).toHaveTextContent("Main model sonnet, Effort high");
    expect(items[0]).toHaveTextContent("house v3");
    expect(items[1]).toHaveTextContent("fast v1");
    expect(items[1]).toHaveTextContent("Main model haiku");
    expect(within(items[1]).getByText("none")).toBeInTheDocument();
  });

  it("shows a saved version as text only, with no way to edit it", async () => {
    mockList([V1]);
    await renderSignedIn(<VariantsWidget />);

    const item = (await screen.findAllByRole("listitem"))[0];
    expect(within(item).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(item).queryByRole("combobox")).not.toBeInTheDocument();
    expect(within(item).getAllByRole("button").map((b) => b.textContent)).toEqual(["New version from this", "Archive"]);
  });

  it("tells a member that only owners see variants, and offers no form", async () => {
    vi.spyOn(apiClient, "listVariants").mockRejectedValue(new ApiError(403, "owners only"));
    await renderSignedIn(<VariantsWidget />);

    expect(await screen.findByText(/only the tenant.s owners/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /save as a new version/i })).not.toBeInTheDocument();
  });

  it("shows a failed load", async () => {
    vi.spyOn(apiClient, "listVariants").mockRejectedValue(new ApiError(500, "database down"));
    await renderSignedIn(<VariantsWidget />);

    expect(await screen.findByRole("alert")).toHaveTextContent("database down");
  });

  describe("saving", () => {
    it("needs a name", async () => {
      mockList([]);
      await renderSignedIn(<VariantsWidget />);

      expect(await screen.findByRole("button", { name: "Save as a new version" })).toBeDisabled();
    });

    it("saves the chosen harness's settings and an explicit none, then reloads", async () => {
      const list = mockList([]);
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(V1);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.type(await screen.findByLabelText("Name"), "fast");
      await user.type(screen.getByRole("combobox", { name: "Main model" }), "haiku");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith("fast", "claude", { model: "haiku" }, null);
      await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    });

    it("has no tenant default option for the fleet instructions, and pins the one chosen", async () => {
      mockList([]);
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(V2);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("combobox", { name: "Fleet instructions" }));
      const listed = within(screen.getByRole("listbox")).getAllByRole("option");
      expect(listed.map((o) => o.textContent)).toEqual(["None", "house v3"]);
      await user.click(screen.getByRole("option", { name: "house v3" }));
      await user.type(screen.getByLabelText("Name"), "fast");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith("fast", "claude", {}, FLEET_ID);
    });

    it("maps choosing None after a version back to no pinned fleet instructions", async () => {
      mockList([]);
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(V1);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await chooseFromDropdown(user, "Fleet instructions", "house v3");
      expect(selectedValueOf("Fleet instructions")).toContain("house v3");
      await chooseFromDropdown(user, "Fleet instructions", "None");
      expect(selectedValueOf("Fleet instructions")).toContain("None");
      await user.type(screen.getByLabelText("Name"), "fast");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith("fast", "claude", {}, null);
    });

    it("keeps the archived fleet instructions a version is pinned to selectable as itself", async () => {
      const pinned = makeVariantVersion({ id: "v3", version: 3, fleet_instructions_version_id: ARCHIVED_FLEET.id });
      mockList([pinned]);
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(pinned);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("button", { name: /Start a new version from/ }));
      expect(selectedValueOf("Fleet instructions")).toContain("old v1 (archived)");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith(pinned.name, pinned.harness, pinned.settings, ARCHIVED_FLEET.id);
    });

    it("sends the settings of the other harness alone after switching", async () => {
      mockList([]);
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(V1);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.type(await screen.findByRole("combobox", { name: "Main model" }), "haiku");
      await chooseFromDropdown(user, "Harness", "pi");
      expect(screen.queryByRole("combobox", { name: "Main model" })).not.toBeInTheDocument();
      await user.type(screen.getByLabelText("Name"), "pi-one");
      await user.type(screen.getByRole("textbox", { name: "Provider" }), "anthropic");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith("pi-one", "pi", { provider: "anthropic" }, null);
    });

    it("starts the next version from a saved one", async () => {
      mockList();
      const save = vi.spyOn(apiClient, "saveVariantVersion").mockResolvedValue(V2);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("button", { name: "Start a new version from fast v2" }));
      expect(screen.getByLabelText("Name")).toHaveValue("fast");
      expect(selectedValueOf("Fleet instructions")).toContain("house v3");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(save).toHaveBeenCalledWith("fast", "claude", { model: "sonnet", effort: "high" }, FLEET_ID);
    });

    it("shows the server's refusal and keeps the form", async () => {
      mockList([]);
      vi.spyOn(apiClient, "saveVariantVersion").mockRejectedValue(new ApiError(422, "unknown setting"));
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.type(await screen.findByLabelText("Name"), "fast");
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("unknown setting");
      expect(screen.getByLabelText("Name")).toHaveValue("fast");
    });
  });

  describe("archiving", () => {
    it("archives one version and reloads", async () => {
      const list = mockList();
      const archive = vi.spyOn(apiClient, "archiveVariantVersion").mockResolvedValue(V1);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive fast v1" }));

      expect(archive).toHaveBeenCalledWith("v1");
      await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    });

    it("archives every version of a name", async () => {
      mockList();
      const archive = vi.spyOn(apiClient, "archiveVariantGroup").mockResolvedValue(2);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive every fast version" }));

      expect(archive).toHaveBeenCalledWith("fast");
    });

    it("lists archived versions only when asked, and can restore one", async () => {
      const archived = makeVariantVersion({ id: "v0", version: 0, archived: true, archived_at: "2026-10-05T12:00:00Z" });
      const list = vi
        .spyOn(apiClient, "listVariants")
        .mockImplementation(async (includeArchived) => (includeArchived ? [archived] : []));
      const unarchive = vi.spyOn(apiClient, "unarchiveVariantVersion").mockResolvedValue(archived);
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      expect(await screen.findByText("No variants yet.")).toBeInTheDocument();
      await user.click(screen.getByRole("checkbox", { name: "Show archived" }));
      await user.click(await screen.findByRole("button", { name: "Unarchive fast v0" }));

      expect(list).toHaveBeenCalledWith(true);
      expect(unarchive).toHaveBeenCalledWith("v0");
    });

    it("shows a refusal without dropping the list", async () => {
      mockList();
      vi.spyOn(apiClient, "archiveVariantVersion").mockRejectedValue(new ApiError(409, "an agent is on it"));
      const user = userEvent.setup();
      await renderSignedIn(<VariantsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive fast v1" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("an agent is on it");
      expect(screen.getAllByRole("listitem")).toHaveLength(2);
    });
  });
});
