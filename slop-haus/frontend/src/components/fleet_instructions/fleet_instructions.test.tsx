import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type FleetInstructions } from "@/api";
import { makeSetting } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import FleetInstructionsWidget from "./fleet_instructions";

afterEach(() => vi.restoreAllMocks());

const V1 = { id: "id-1", name: "house", version: 1, kind: "fleet" as const, body: "Be brief.", created_by: null, created_at: "2026-10-04T12:00:00Z", archived: false, archived_at: null };
const V2 = { ...V1, id: "id-2", version: 2, body: "Comment a plan first." };

function data(overrides: Partial<FleetInstructions>): FleetInstructions {
  return { versions: [], default_id: null, can_edit: true, max_body_bytes: 100, ...overrides };
}

function mockGet(overrides: Partial<FleetInstructions>) {
  return vi.spyOn(apiClient, "getFleetInstructions").mockResolvedValue(data(overrides));
}

describe("FleetInstructionsWidget", () => {
  it("says no default is chosen, and that briefings then carry nothing", async () => {
    mockGet({});
    await renderSignedIn(<FleetInstructionsWidget />);

    expect(await screen.findByText(/no fleet instructions/i)).toBeInTheDocument();
    expect(document.querySelector(".fleet-instructions__badge")).toBeNull();
  });

  it("lists every version, newest first within a name, and marks the default", async () => {
    mockGet({ versions: [V2, V1], default_id: "id-1" });
    await renderSignedIn(<FleetInstructionsWidget />);

    const items = await screen.findAllByRole("listitem");
    expect(items[0]).toHaveTextContent("house v2");
    expect(items[1]).toHaveTextContent("house v1");
    expect(items[1].querySelector(".fleet-instructions__badge")).toHaveTextContent("default");
    expect(items[0].querySelector(".fleet-instructions__badge")).toBeNull();
  });

  describe("collapsible history", () => {
    it("opens the default version and folds the others", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-1" });
      await renderSignedIn(<FleetInstructionsWidget />);

      expect(await screen.findByText("Be brief.")).toBeInTheDocument();
      expect(screen.queryByText("Comment a plan first.")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Collapse house v1" })).toHaveAttribute("aria-expanded", "true");
      expect(screen.getByRole("button", { name: "Expand house v2" })).toHaveAttribute("aria-expanded", "false");
    });

    it("folds everything when there is no default", async () => {
      mockGet({ versions: [V2, V1] });
      await renderSignedIn(<FleetInstructionsWidget />);

      await screen.findByRole("button", { name: "Expand house v1" });
      expect(screen.queryByText("Be brief.")).not.toBeInTheDocument();
      expect(screen.queryByText("Comment a plan first.")).not.toBeInTheDocument();
    });

    it("opens and closes one version without touching the others", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-1" });
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Expand house v2" }));
      expect(screen.getByText("Comment a plan first.")).toBeInTheDocument();
      expect(screen.getByText("Be brief.")).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Collapse house v1" }));
      expect(screen.queryByText("Be brief.")).not.toBeInTheDocument();
      expect(screen.getByText("Comment a plan first.")).toBeInTheDocument();
    });

    it("folds the whole history, and shows it again", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-1" });
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      const toggle = await screen.findByRole("button", { name: /Hide version history \(2\)/ });
      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("list")).not.toBeInTheDocument();
      expect(screen.queryByText("Be brief.")).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: /Show version history/ }));
      expect(screen.getByText("Be brief.")).toBeInTheDocument();
    });

    it("hides a folded version's controls", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-1" });
      await renderSignedIn(<FleetInstructionsWidget />);

      await screen.findByRole("button", { name: "Expand house v2" });
      expect(screen.queryByRole("button", { name: "Make house v2 the default" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Start a new version from house v1" })).toBeInTheDocument();
    });
  });

  it("shows the text as plain text, not markup", async () => {
    mockGet({ versions: [{ ...V1, body: "<b>bold</b> <img src=x onerror=alert(1)>" }], default_id: "id-1" });
    const { container } = await renderSignedIn(<FleetInstructionsWidget />);

    await screen.findByText(/<b>bold<\/b>/);
    expect(container.querySelector("b, img")).toBeNull();
  });

  it("saves a new version without choosing it unless asked", async () => {
    mockGet({});
    const save = vi.spyOn(apiClient, "saveFleetInstructionsVersion").mockResolvedValue(V1);
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    await user.type(await screen.findByLabelText("Name"), "house");
    await user.type(screen.getByLabelText("Instructions"), "Be brief.");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));
    expect(save).toHaveBeenLastCalledWith("house", "Be brief.", false, "fleet");

    await user.type(screen.getByLabelText("Instructions"), "Again.");
    await user.click(screen.getByLabelText(/Make this version the default/));
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));
    expect(save).toHaveBeenLastCalledWith("house", "Again.", true, "fleet");
  });

  // jsdom loads no scss, so what can be pinned is the markup that picks up
  // the shared look: @slop-factory/ui's Button and the form-field classes.
  it("uses the shared Button and styled fields rather than bare controls", async () => {
    mockGet({ versions: [V1], default_id: "id-1" });
    await renderSignedIn(<FleetInstructionsWidget />);

    await screen.findByLabelText("Name");
    const buttons = screen.getAllByRole("button");
    expect(buttons.length).toBeGreaterThanOrEqual(4);
    for (const b of buttons) expect(b).toHaveClass("button");
    expect(screen.getByLabelText("Name").closest("label")).toHaveClass("fleet-instructions__field");
    expect(screen.getByLabelText("Instructions").closest("label")).toHaveClass("fleet-instructions__field");
    expect(screen.getByLabelText(/Make this version the default/).closest("label")).toHaveClass(
      "fleet-instructions__checkbox",
    );
  });

  it("starts a new version from an old one", async () => {
    mockGet({ versions: [V1], default_id: "id-1" });
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    await user.click(await screen.findByRole("button", { name: "Start a new version from house v1" }));
    expect(screen.getByLabelText("Name")).toHaveValue("house");
    expect(screen.getByLabelText("Instructions")).toHaveValue("Be brief.");
  });

  it("picks the default, and clears it", async () => {
    mockGet({ versions: [V2, V1], default_id: "id-1" });
    const choose = vi.spyOn(apiClient, "setFleetInstructionsDefault").mockResolvedValue();
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    expect(screen.queryByRole("button", { name: "Make house v1 the default" })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Expand house v2" }));
    await user.click(await screen.findByRole("button", { name: "Make house v2 the default" }));
    expect(choose).toHaveBeenLastCalledWith("id-2", "fleet");

    await user.click(screen.getByRole("button", { name: "Use no fleet instructions" }));
    expect(choose).toHaveBeenLastCalledWith(null, "fleet");
  });

  it("shows the new default after choosing one", async () => {
    vi.spyOn(apiClient, "getFleetInstructions")
      .mockResolvedValueOnce(data({ versions: [V2, V1], default_id: "id-1" }))
      .mockResolvedValue(data({ versions: [V2, V1], default_id: "id-2" }));
    vi.spyOn(apiClient, "setFleetInstructionsDefault").mockResolvedValue();
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    await user.click(await screen.findByRole("button", { name: "Expand house v2" }));
    await user.click(screen.getByRole("button", { name: "Make house v2 the default" }));

    await waitFor(() =>
      expect(screen.getAllByRole("listitem")[0].querySelector(".fleet-instructions__badge")).toHaveTextContent(
        "default",
      ),
    );
    expect(screen.getAllByRole("listitem")[1].querySelector(".fleet-instructions__badge")).toBeNull();
  });

  it("says so when the instructions cannot be loaded", async () => {
    vi.spyOn(apiClient, "getFleetInstructions").mockRejectedValue(new ApiError(500, "boom"));
    await renderSignedIn(<FleetInstructionsWidget />);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });

  it("refuses text over the limit before sending it", async () => {
    mockGet({ max_body_bytes: 5 });
    const save = vi.spyOn(apiClient, "saveFleetInstructionsVersion");
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    await user.type(await screen.findByLabelText("Name"), "n");
    await user.type(screen.getByLabelText("Instructions"), "héllo!");
    expect(screen.getByText("7 / 5 bytes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save as a new version" })).toBeDisabled();
    expect(save).not.toHaveBeenCalled();
  });

  it("shows the server's refusal", async () => {
    mockGet({});
    vi.spyOn(apiClient, "saveFleetInstructionsVersion").mockRejectedValue(new ApiError(403, "only owners"));
    const user = userEvent.setup();
    await renderSignedIn(<FleetInstructionsWidget />);

    await user.type(await screen.findByLabelText("Name"), "n");
    await user.type(screen.getByLabelText("Instructions"), "x");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("only owners");
    // A failed save reports false, so the draft stays for another try.
    expect(screen.getByLabelText("Instructions")).toHaveValue("x");
  });

  it("is read only for anyone but an owner", async () => {
    mockGet({ versions: [V1], default_id: "id-1", can_edit: false });
    await renderSignedIn(<FleetInstructionsWidget />);

    expect(await screen.findByText(/Only the tenant.s owners/)).toBeInTheDocument();
    // Reading the briefing is for anyone; every other button changes something.
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Fleet",
      "Steward",
      "Sensitive words",
      "Show the briefing agents receive",
      "Hide version history (1)",
      "house v1",
    ]);
    expect(screen.queryByLabelText("Instructions")).not.toBeInTheDocument();
  });

  describe("archiving", () => {
    const OLD = { ...V1, id: "id-old", name: "legacy", body: "Old text.", archived: true, archived_at: "2026-10-05T12:00:00Z" };

    it("hides archived versions until 'Show archived' is ticked, then marks them", async () => {
      const get = vi.spyOn(apiClient, "getFleetInstructions").mockImplementation(async (includeArchived) =>
        data({ versions: includeArchived ? [V1, OLD] : [V1], default_id: "id-1" }),
      );
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      const box = await screen.findByRole("checkbox", { name: "Show archived" });
      expect(box).not.toBeChecked();
      expect(get).toHaveBeenLastCalledWith(false, "fleet");
      expect(screen.queryByText("legacy")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /Unarchive/ })).not.toBeInTheDocument();

      await user.click(box);
      const entry = (await screen.findByRole("button", { name: "Expand legacy v1" })).closest("li")!;
      expect(get).toHaveBeenLastCalledWith(true, "fleet");
      expect(entry.querySelector(".fleet-instructions__badge")).toHaveTextContent("archived");
      expect(screen.getByRole("button", { name: "Unarchive legacy v1" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Unarchive every legacy version" })).toBeInTheDocument();

      await user.click(box);
      await waitFor(() => expect(screen.queryByText("legacy")).not.toBeInTheDocument());
    });

    it("unarchives a version and a name group", async () => {
      mockGet({ versions: [V1, OLD], default_id: "id-1" });
      const unarchive = vi.spyOn(apiClient, "unarchiveFleetInstructionsVersion").mockResolvedValue(OLD);
      const unarchiveGroup = vi.spyOn(apiClient, "unarchiveFleetInstructionsGroup").mockResolvedValue(1);
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Unarchive legacy v1" }));
      expect(unarchive).toHaveBeenCalledWith("id-old");
      await user.click(screen.getByRole("button", { name: "Unarchive every legacy version" }));
      expect(unarchiveGroup).toHaveBeenCalledWith("legacy", "fleet");
    });

    it("archives an ordinary version and group", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-9" });
      const archive = vi.spyOn(apiClient, "archiveFleetInstructionsVersion").mockResolvedValue(V2);
      const archiveGroup = vi.spyOn(apiClient, "archiveFleetInstructionsGroup").mockResolvedValue(2);
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive house v2" }));
      expect(archive).toHaveBeenCalledWith("id-2");
      await user.click(screen.getByRole("button", { name: "Archive every house version" }));
      expect(archiveGroup).toHaveBeenCalledWith("house", "fleet");
    });

    it("explains why the default and its group can't be archived, and does not call the server", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-1" });
      const archive = vi.spyOn(apiClient, "archiveFleetInstructionsVersion");
      const archiveGroup = vi.spyOn(apiClient, "archiveFleetInstructionsGroup");
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      const one = await screen.findByRole("button", { name: "Archive house v1" });
      const group = screen.getByRole("button", { name: "Archive every house version" });
      expect(one).toHaveAttribute("aria-disabled", "true");
      expect(group).toHaveAttribute("aria-disabled", "true");
      // Still focusable, so the reason is reachable from the keyboard.
      one.focus();
      expect(await screen.findByRole("tooltip")).toHaveTextContent(/default can't be archived/);
      expect(one).toHaveAccessibleDescription(/default can't be archived/);
      await user.click(one);
      await user.click(group);
      expect(archive).not.toHaveBeenCalled();
      expect(archiveGroup).not.toHaveBeenCalled();
    });

    it("shows the server's refusal when archiving the default is rejected", async () => {
      mockGet({ versions: [V2, V1], default_id: "id-9" });
      vi.spyOn(apiClient, "archiveFleetInstructionsVersion").mockRejectedValue(
        new ApiError(409, "the default version can't be archived"),
      );
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive house v2" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("the default version can't be archived");
      expect(screen.getByRole("button", { name: "Archive house v2" })).toBeInTheDocument();
    });

    it("offers no archive controls to anyone but an owner", async () => {
      mockGet({ versions: [V1, OLD], default_id: "id-1", can_edit: false });
      await renderSignedIn(<FleetInstructionsWidget />);

      await screen.findByRole("checkbox", { name: "Show archived" });
      expect(screen.queryByRole("button", { name: /archive/i })).not.toBeInTheDocument();
    });

    it("keeps the checkbox when every version is archived, and shows them when ticked", async () => {
      vi.spyOn(apiClient, "getFleetInstructions").mockImplementation(async (includeArchived) =>
        data({ versions: includeArchived ? [OLD] : [] }),
      );
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("checkbox", { name: "Show archived" }));
      expect(await screen.findByRole("button", { name: "Expand legacy v1" })).toBeInTheDocument();
    });

    it("reloads after archiving, so the version leaves the list", async () => {
      let archived = false;
      vi.spyOn(apiClient, "getFleetInstructions").mockImplementation(async () =>
        data({ versions: archived ? [V1] : [V2, V1], default_id: "id-1" }),
      );
      vi.spyOn(apiClient, "archiveFleetInstructionsVersion").mockImplementation(async () => {
        archived = true;
        return V2;
      });
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Archive house v2" }));
      await waitFor(() => expect(screen.queryByRole("button", { name: "Expand house v2" })).not.toBeInTheDocument());
      expect(screen.getByRole("button", { name: "Collapse house v1" })).toBeInTheDocument();
    });

    it("reloads after unarchiving, so the version becomes live", async () => {
      let live = false;
      vi.spyOn(apiClient, "getFleetInstructions").mockImplementation(async () =>
        data({ versions: [{ ...OLD, archived: !live }], default_id: "id-1" }),
      );
      vi.spyOn(apiClient, "unarchiveFleetInstructionsVersion").mockImplementation(async () => {
        live = true;
        return OLD;
      });
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      await user.click(await screen.findByRole("button", { name: "Unarchive legacy v1" }));
      expect(await screen.findByRole("button", { name: "Archive legacy v1" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Unarchive legacy v1" })).not.toBeInTheDocument();
    });
  });

  describe("briefing preview", () => {
    const files = [
      { name: "headless", path: ".slop-haus/headless-briefing.md", protocol: "# slop-haus agent: <agent-name>\n", fleet: "\n## Fleet instructions\n\nThe protocol takes precedence.\n\n> Be brief.\n>\n> Then stop.\n" },
      { name: "pi", path: ".pi/slop-haus.md", protocol: "pi briefing\n", fleet: "" },
    ];

    function mockBriefings() {
      return vi.spyOn(apiClient, "getFleetBriefings").mockResolvedValue(files);
    }

    it("is collapsed by default and asks the server for nothing until opened", async () => {
      mockGet({ versions: [V1], default_id: "id-1" });
      const briefings = mockBriefings();
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);

      const toggle = await screen.findByRole("button", { name: /briefing agents receive/ });
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("tablist")).toBeNull();
      expect(briefings).not.toHaveBeenCalled();

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "true");
      expect(await screen.findByRole("tablist")).toBeInTheDocument();
      expect(briefings).toHaveBeenCalledWith("id-1");
    });

    it("has one tab per file, and switching shows that file", async () => {
      mockGet({ versions: [V1], default_id: "id-1" });
      mockBriefings();
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));

      const tabs = await screen.findAllByRole("tab");
      expect(tabs.map((t) => t.textContent)).toEqual(["Headless run", "pi"]);
      expect(screen.getByRole("tabpanel")).toHaveTextContent("slop-haus agent: <agent-name>");

      await user.click(screen.getByRole("tab", { name: "pi" }));
      expect(screen.getByRole("tab", { name: "pi" })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByRole("tabpanel")).toHaveTextContent("pi briefing");
      expect(screen.getByRole("tabpanel")).not.toHaveTextContent("<agent-name>");
    });

    it("highlights the owners' quoted text but not the wrapper around it", async () => {
      mockGet({ versions: [V1], default_id: "id-1" });
      mockBriefings();
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));

      const section = await screen.findByTestId("fleet-section");
      expect(section).toHaveTextContent("The protocol takes precedence.");
      const marked = [...section.querySelectorAll("mark")].map((m) => m.textContent);
      expect(marked).toEqual(["> Be brief.\n", ">\n", "> Then stop.\n"]);
    });

    it("asks for the version picked, or for none", async () => {
      mockGet({ versions: [V1, V2], default_id: "id-1" });
      const briefings = mockBriefings();
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));
      await screen.findByRole("tablist");

      await user.click(screen.getByRole("combobox", { name: "Fleet instructions shown" }));
      await user.click(screen.getByRole("option", { name: /house v2/ }));
      expect(briefings).toHaveBeenLastCalledWith("id-2");
      await user.click(screen.getByRole("combobox", { name: "Fleet instructions shown" }));
      await user.click(screen.getByRole("option", { name: "None" }));
      expect(briefings).toHaveBeenLastCalledWith(null);
    });

    it("shows the file for the version picked, not the one before", async () => {
      mockGet({ versions: [V1, V2], default_id: "id-1" });
      vi.spyOn(apiClient, "getFleetBriefings").mockImplementation((id) =>
        Promise.resolve([{ ...files[0], protocol: `briefing for ${id}\n` }]),
      );
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));
      expect(await screen.findByRole("tabpanel")).toHaveTextContent("briefing for id-1");

      await user.click(screen.getByRole("combobox", { name: "Fleet instructions shown" }));
      await user.click(screen.getByRole("option", { name: /house v2/ }));
      expect(await screen.findByText(/briefing for id-2/)).toBeInTheDocument();
    });

    it("says so when the briefing cannot be loaded", async () => {
      mockGet({});
      vi.spyOn(apiClient, "getFleetBriefings").mockRejectedValue(new ApiError(500, "boom"));
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));

      expect(await screen.findByText("Failed to load the briefing.")).toBeInTheDocument();
      expect(screen.queryByRole("tablist")).toBeNull();
    });

    it("says what it is and is not", async () => {
      mockGet({});
      mockBriefings();
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: /briefing agents receive/ }));

      expect(await screen.findByText(/current server would write/)).toHaveTextContent(/CLAUDE\.md/);
    });
  });

  describe("kinds", () => {
    const WORDS = { ...V1, id: "w1", kind: "sensitive_words" as const, name: "words", body: "secret\npassword" };
    const level = (n: number) =>
      makeSetting({ name: "steward_level", type: "int", int_val: n, bool_val: null, is_default: false });

    it("shows the three kinds, fleet first, and asks the API for the chosen one", async () => {
      const get = mockGet({});
      await renderSignedIn(<FleetInstructionsWidget />);

      const tabs = await screen.findAllByRole("button", { name: /^(Fleet|Steward|Sensitive words)$/ });
      expect(tabs.map((t) => t.textContent)).toEqual(["Fleet", "Steward", "Sensitive words"]);
      expect(tabs[0]).toHaveAttribute("aria-current", "page");
      expect(get).toHaveBeenLastCalledWith(false, "fleet");

      await userEvent.click(screen.getByRole("button", { name: "Steward" }));
      await waitFor(() => expect(get).toHaveBeenLastCalledWith(false, "steward"));
      expect(screen.getByRole("button", { name: "Steward" })).toHaveAttribute("aria-current", "page");
    });

    it("explains that steward instructions cannot loosen the server's checks", async () => {
      mockGet({});
      await renderSignedIn(<FleetInstructionsWidget />);
      await userEvent.click(await screen.findByRole("button", { name: "Steward" }));

      expect(
        await screen.findByText(/can.t loosen the server.s checks \(protected paths, size limit, CI, the sensitive word list\)/),
      ).toBeInTheDocument();
      expect(screen.queryByText(/briefing agents receive/)).not.toBeInTheDocument();
    });

    it("explains the word list format, and saves under the sensitive_words kind, even empty", async () => {
      mockGet({});
      vi.spyOn(apiClient, "listSettings").mockResolvedValue([]);
      const save = vi.spyOn(apiClient, "saveFleetInstructionsVersion").mockResolvedValue(WORDS);
      await renderSignedIn(<FleetInstructionsWidget />);
      await userEvent.click(await screen.findByRole("button", { name: "Sensitive words" }));

      expect(await screen.findByText(/one word or phrase per line.*case-insensitive.*whole words/i)).toBeInTheDocument();
      await userEvent.type(screen.getByLabelText("Name"), "words");
      await userEvent.click(screen.getByRole("button", { name: "Save as a new version" }));
      expect(save).toHaveBeenCalledWith("words", "", false, "sensitive_words");
    });

    it("keeps an empty body unsavable on the fleet kind", async () => {
      mockGet({});
      await renderSignedIn(<FleetInstructionsWidget />);
      await userEvent.type(await screen.findByLabelText("Name"), "house");
      expect(screen.getByRole("button", { name: "Save as a new version" })).toBeDisabled();
    });

    it.each([
      ["none is pinned", { versions: [WORDS], default_id: null }, 4, true],
      ["the pinned list is blank", { versions: [{ ...WORDS, body: "  " }], default_id: "w1" }, 5, true],
      ["a list is pinned", { versions: [WORDS], default_id: "w1" }, 5, false],
      ["judgment stays with a human", { versions: [], default_id: null }, 3, false],
    ])("warns about the word list only when needed: %s", async (_name, over, lvl, warns) => {
      mockGet(over);
      vi.spyOn(apiClient, "listSettings").mockResolvedValue([level(lvl)]);
      await renderSignedIn(<FleetInstructionsWidget />);
      await userEvent.click(await screen.findByRole("button", { name: "Sensitive words" }));
      await screen.findByText(/one word or phrase per line/i);

      if (warns) expect(await screen.findByRole("alert")).toHaveTextContent(/no sensitive words are pinned/i);
      else {
        await waitFor(() => expect(apiClient.listSettings).toHaveBeenCalled());
        expect(screen.queryByText(/no sensitive words are pinned/i)).not.toBeInTheDocument();
      }
    });

    it("routes the default and group actions to the chosen kind", async () => {
      const STEWARD = { ...V1, id: "s1", kind: "steward" as const };
      mockGet({ versions: [STEWARD], default_id: null });
      const setDefault = vi.spyOn(apiClient, "setFleetInstructionsDefault").mockResolvedValue();
      const group = vi.spyOn(apiClient, "archiveFleetInstructionsGroup").mockResolvedValue(1);
      const user = userEvent.setup();
      await renderSignedIn(<FleetInstructionsWidget />);
      await user.click(await screen.findByRole("button", { name: "Steward" }));

      await user.click(await screen.findByRole("button", { name: "Expand house v1" }));
      await user.click(screen.getByRole("button", { name: "Make house v1 the default" }));
      expect(setDefault).toHaveBeenCalledWith("s1", "steward");
      await user.click(screen.getByRole("button", { name: "Archive every house version" }));
      expect(group).toHaveBeenCalledWith("house", "steward");
    });

    it("leaves the word list warning out when the settings cannot be read", async () => {
      mockGet({});
      const settings = vi.spyOn(apiClient, "listSettings").mockRejectedValue(new ApiError(500, "boom"));
      await renderSignedIn(<FleetInstructionsWidget />);
      await userEvent.click(await screen.findByRole("button", { name: "Sensitive words" }));
      await waitFor(() => expect(settings).toHaveBeenCalled());
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });
});
