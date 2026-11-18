import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type FleetInstructions, type StewardDigest, type StewardDigestCategory, type Workspace } from "@/api";
import { makeAgent, makeHarnessConfig, makeHarnessConfigScope, makeSetting, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { chooseFromDropdown } from "@/test/widgets";
import TenantSettingsPage from "./tenant_settings_page";
import { STEWARD_CATEGORIES } from "./steward_settings";

const mode = (string_val = "off") =>
  makeSetting({
    name: "steward_mode",
    type: "string",
    string_val,
    bool_val: null,
  });
const level = (int_val = -1) =>
  makeSetting({
    name: "steward_level",
    type: "int",
    int_val,
    bool_val: null,
    is_default: int_val < 0,
  });
const veto = (int_val = 60) =>
  makeSetting({
    name: "steward_veto_minutes",
    type: "int",
    int_val,
    bool_val: null,
  });
const cap = (int_val = 50) =>
  makeSetting({
    name: "steward_daily_cap",
    type: "int",
    int_val,
    bool_val: null,
  });
const tiers = (string_val = "{}") =>
  makeSetting({
    name: "steward_category_tiers",
    type: "string",
    string_val,
    bool_val: null,
  });

const digest = (categories: Partial<StewardDigestCategory>[] = [], decisions: StewardDigest["decisions"] = []) =>
  ({
    categories,
    decisions,
    decisions_cut: false,
    since: "",
    until: "",
    total: {},
  }) as unknown as StewardDigest;

const reviewMax = (int_val = 400) =>
  makeSetting({ name: "steward_review_max_lines", type: "int", int_val, bool_val: null });

function stub({
  isOwner = true,
  steward = [mode(), veto(), cap(), tiers()],
  stewardAgent = makeAgent({ id: "s1", kind: "steward" }),
  stewardDigest = digest(),
  protectedRules = 1,
  wordList = { default_id: "w1", versions: [{ id: "w1", body: "salary\nlayoffs" }] } as {
    default_id: string | null;
    versions: { id: string; body: string }[];
  },
} = {}) {
  vi.spyOn(apiClient, "getFleetInstructions").mockResolvedValue(wordList as unknown as FleetInstructions);
  vi.spyOn(apiClient, "getStewardDigest").mockResolvedValue(stewardDigest);
  vi.spyOn(apiClient, "getWorkspace").mockResolvedValue({
    manifest: {
      repos: [
        {
          path: "r",
          protected_paths: Array(protectedRules).fill({ path: "x/" }),
        },
      ],
    },
  } as unknown as Workspace);
  vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig({ defaults: makeHarnessConfigScope({ settings: { claude: { model: "sonnet" } } }) }));
  vi.spyOn(apiClient, "listAgents").mockResolvedValue([stewardAgent]);
  vi.spyOn(apiClient, "listSettings").mockResolvedValue([makeSetting(), ...steward]);
  vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([]);
  vi.spyOn(apiClient, "getSettingHistory").mockResolvedValue({ changes: [] });
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: isOwner })]);
}

async function steward() {
  return within(await screen.findByRole("region", { name: "Steward" }));
}

describe("the Steward section", () => {
  it("shows the model the steward runs on, where it comes from, and a link to its config", async () => {
    stub();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(await section.findByText(/Model: sonnet \(tenant default\)/)).toBeInTheDocument();
    expect(section.getByRole("link", { name: "Workers page" })).toHaveAttribute("href", "/workers");
  });

  it("shows what the supervisor reported about the steward, such as why it did not start", async () => {
    stub({
      stewardAgent: makeAgent({
        id: "s1",
        kind: "steward",
        observed: { phase: "failed", reason: "Codex steward not started: sandbox probe refused `network` - no netns", reported_at: "2026-10-29T09:00:00Z" },
      }),
    });
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(await section.findByText(/Codex steward not started: sandbox probe refused `network` - no netns/)).toBeInTheDocument();
  });

  it("shows no steward status when nothing was reported", async () => {
    stub();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await section.findByText(/Model: sonnet/);
    expect(section.queryByRole("status")).toBeNull();
  });

  it("is shown to an owner and not to a member", async () => {
    stub({ isOwner: false });
    await renderSignedIn(<TenantSettingsPage />);

    await screen.findByRole("region", { name: "This tenant" });
    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "Steward" })).toBeNull();
  });

  it("keeps the raw steward keys out of the generic list", async () => {
    stub();
    await renderSignedIn(<TenantSettingsPage />);

    const tenant = within(await screen.findByRole("region", { name: "This tenant" }));
    await tenant.findByRole("switch", { name: "Ticket processing" });
    expect(tenant.queryByText(/steward_/)).toBeNull();
  });

  it("falls back to the stored mode until a level is written, and writes the level", async () => {
    stub({ steward: [mode("shadow"), level(), veto(), cap(), tiers()] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(2));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(section.getByRole("radio", { name: /1 Shadow/ })).toBeChecked();
    expect(section.getByText(/Example: A drafted answer/)).toBeInTheDocument();
    expect(section.getByText(/human-with-draft/)).toBeInTheDocument();
    await user.click(section.getByRole("radio", { name: /2 Assist/ }));

    expect(update).toHaveBeenCalledWith("steward_level", { type: "int", int_val: 2 });
    await vi.waitFor(() => expect(section.getByRole("radio", { name: /2 Assist/ })).toBeChecked());
  });

  it("moves the shown tiers with the level once it is saved", async () => {
    stub({ steward: [mode(), level(2), veto(), cap(), tiers()] });
    vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(3));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    const merge = () => section.getByRole("combobox", { name: "Tell an agent to carry on and merge" });
    expect(merge()).toHaveTextContent("human");
    await user.click(section.getByRole("radio", { name: /3 Routine/ }));
    await vi.waitFor(() => expect(merge()).toHaveTextContent("auto"));
  });

  it("follows a deprecated mode written after the level", async () => {
    const newer = makeSetting({
      name: "steward_mode",
      type: "string",
      string_val: "off",
      bool_val: null,
      updated_at: "2026-10-02T00:00:00Z",
    });
    const older = { ...level(5), updated_at: "2026-10-01T00:00:00Z" };
    stub({ steward: [newer, older, veto(), cap(), tiers()] });
    await renderSignedIn(<TenantSettingsPage />);

    expect((await steward()).getByRole("radio", { name: /0 Off/ })).toBeChecked();
  });

  it("confirms a level of 4 or more, naming the change, and writes nothing on cancel", async () => {
    stub({ steward: [mode(), level(3), veto(), cap(), tiers()] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(5));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await user.click(section.getByRole("radio", { name: /5 Full delegation/ }));
    const dialog = section.getByRole("alertdialog");
    expect(dialog).toHaveTextContent("Level 3 -> 5");
    expect(dialog).toHaveTextContent("no human look");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(update).not.toHaveBeenCalled();
    expect(section.getByRole("radio", { name: /3 Routine/ })).toBeChecked();

    await user.click(section.getByRole("radio", { name: /5 Full delegation/ }));
    await user.click(
      within(section.getByRole("alertdialog")).getByRole("button", {
        name: "Save",
      }),
    );
    expect(update).toHaveBeenCalledWith("steward_level", {
      type: "int",
      int_val: 5,
    });
  });

  it("saves a level below 4 without asking", async () => {
    stub({ steward: [mode(), level(3), veto(), cap(), tiers()] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(2));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await user.click(section.getByRole("radio", { name: /2 Assist/ }));
    expect(section.queryByRole("alertdialog")).toBeNull();
    expect(update).toHaveBeenCalled();
  });

  it("shows the server's refusal of a level and keeps the stored one", async () => {
    stub();
    vi.spyOn(apiClient, "updateSetting").mockRejectedValue(new ApiError(403, "only an owner may change this"));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await user.click(section.getByRole("radio", { name: /1 Shadow/ }));

    expect(await section.findByRole("alert")).toHaveTextContent("only an owner may change this");
    expect(section.getByRole("radio", { name: /0 Off/ })).toBeChecked();
  });

  it("shows the agreement record beside each tier, with the latest disagreements linked", async () => {
    stub({
      steward: [mode("live"), level(3), veto(), cap(), tiers()],
      stewardDigest: digest(
        [{ category: "priority", assessed: 30, agreement_rate: 0.9 }],
        [
          {
            category: "priority",
            outcome: "vetoed",
            proposal_id: "p1",
            ticket_id: 77,
            at: "",
          },
        ],
      ),
    });
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(await section.findByText(/30 decisions, 90% agreement/)).toBeInTheDocument();
    expect(section.getByRole("link", { name: "#77" })).toBeInTheDocument();
    expect(section.getAllByText("No decisions yet").length).toBeGreaterThan(0);
  });

  it("explains an empty agreement record from the digest's own counts", async () => {
    const empty = digest();
    empty.total = {
      category: "all",
      assessed: 77,
      applied: 0,
      vetoed: 0,
      undone: 0,
      needs_human: 75,
      awaiting: 2,
      unclassified: 43,
      agreed: 0,
      disagreed: 0,
      agreement_rate: null,
    };
    stub({ steward: [mode("shadow"), level(3), veto(), cap(), tiers()], stewardDigest: empty });
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(
      await section.findByText("77 assessed in the last 7 days — 75 yours by design (43 unclassified), 2 awaiting your decision"),
    ).toBeInTheDocument();
  });

  it("warns, without blocking, about thin or poor agreement and unrun shadow", async () => {
    stub({
      steward: [mode("live"), level(3), veto(), cap(), tiers()],
      stewardDigest: digest([{ category: "priority", assessed: 5, agreement_rate: 0.8 }]),
    });
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    const warnings = within(await section.findByRole("list", { name: "Warnings" }));
    await warnings.findByText(
      /Change a ticket's priority: 5 decisions so far \(suggested at least 20\), agreement 80% \(suggested at least 95%\)/,
    );
    expect(warnings.getByText(/Narrow a ticket's paths: shadow has not run/)).toBeInTheDocument();
    expect(warnings.queryByText(/Open a preparing ticket: shadow/)).toBeNull();
    expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toBeEnabled();
  });

  describe("the sensitive word list warning", () => {
    const emptyWarning = /The sensitive word list is empty or none is pinned/;
    const raised = { steward: [mode("live"), level(4), veto(), cap(), tiers()] };

    it("warns when no list is pinned", async () => {
      stub({ ...raised, wordList: { default_id: null, versions: [{ id: "w1", body: "salary" }] } });
      await renderSignedIn(<TenantSettingsPage />);

      const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
      expect(await warnings.findByText(emptyWarning)).toBeInTheDocument();
      expect(apiClient.getFleetInstructions).toHaveBeenCalledWith(false, "sensitive_words");
    });

    it("warns when the pinned list is blank", async () => {
      stub({ ...raised, wordList: { default_id: "w1", versions: [{ id: "w1", body: " \n\t" }] } });
      await renderSignedIn(<TenantSettingsPage />);

      const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
      expect(await warnings.findByText(emptyWarning)).toBeInTheDocument();
    });

    it("does not warn when the pinned list has words", async () => {
      stub(raised);
      await renderSignedIn(<TenantSettingsPage />);

      const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
      await warnings.findAllByText(/shadow has not run/);
      expect(warnings.queryByText(emptyWarning)).toBeNull();
    });

    it("reads the pinned version, not the first one", async () => {
      stub({
        ...raised,
        wordList: {
          default_id: "w2",
          versions: [
            { id: "w1", body: "" },
            { id: "w2", body: "salary" },
          ],
        },
      });
      await renderSignedIn(<TenantSettingsPage />);

      const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
      await warnings.findAllByText(/shadow has not run/);
      expect(warnings.queryByText(emptyWarning)).toBeNull();
    });

    it("leaves the warning out when the list could not be read", async () => {
      stub(raised);
      vi.spyOn(apiClient, "getFleetInstructions").mockRejectedValue(new ApiError(500, "boom"));
      await renderSignedIn(<TenantSettingsPage />);

      const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
      await warnings.findAllByText(/shadow has not run/);
      expect(warnings.queryByText(emptyWarning)).toBeNull();
    });
  });

  it("warns on either condition alone, against the configured thresholds", async () => {
    const warnN = makeSetting({ name: "steward_warn_min_decisions", type: "int", int_val: 10, bool_val: null });
    const warnX = makeSetting({ name: "steward_warn_min_agreement_pct", type: "int", int_val: 50, bool_val: null });
    stub({
      steward: [mode("live"), level(3), warnN, warnX, veto(), cap(), tiers()],
      stewardDigest: digest([
        { category: "priority", assessed: 50, agreement_rate: 0.4 },
        { category: "narrow_scope", assessed: 5, agreement_rate: 1 },
        { category: "ticket_ready", assessed: 12, agreement_rate: 0.6 },
      ]),
    });
    await renderSignedIn(<TenantSettingsPage />);

    const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
    await warnings.findByText(
      /Change a ticket's priority: 50 decisions so far \(suggested at least 10\), agreement 40%/,
    );
    expect(warnings.getByText(/Narrow a ticket's paths: 5 decisions so far/)).toBeInTheDocument();
    expect(warnings.queryByText(/Open a preparing ticket: 12 decisions/)).toBeNull();
  });

  it("warns that the steward is off", async () => {
    stub();
    await renderSignedIn(<TenantSettingsPage />);

    const warnings = within(await (await steward()).findByRole("list", { name: "Warnings" }));
    expect(warnings.getByText(/The steward is off/)).toBeInTheDocument();
  });

  it("writes the warning thresholds", async () => {
    const warnN = makeSetting({
      name: "steward_warn_min_decisions",
      type: "int",
      int_val: 20,
      bool_val: null,
    });
    const warnX = makeSetting({
      name: "steward_warn_min_agreement_pct",
      type: "int",
      int_val: 95,
      bool_val: null,
    });
    stub({ steward: [mode(), level(0), warnN, warnX, veto(), cap(), tiers()] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(warnN);
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    const field = section.getByRole("spinbutton", {
      name: "Warn below agreement",
    });
    expect(field).toHaveValue(95);
    await user.clear(field);
    await user.type(field, "99");
    await user.click(section.getAllByRole("button", { name: "Save" })[1]);
    expect(update).toHaveBeenCalledWith("steward_warn_min_agreement_pct", {
      type: "int",
      int_val: 99,
    });
  });

  it("validates the veto window and the daily cap against the server's bounds", async () => {
    stub();
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(veto(90));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    const field = section.getByRole("spinbutton", { name: "Veto window" });
    expect(field).toHaveValue(60);
    await user.clear(field);
    await user.type(field, "4");
    const save = section.getAllByRole("button", { name: "Save" })[0];
    expect(save).toBeDisabled();
    await user.clear(field);
    await user.type(field, "90");
    await user.click(save);
    expect(update).toHaveBeenCalledWith("steward_veto_minutes", {
      type: "int",
      int_val: 90,
    });

    const capField = section.getByRole("spinbutton", { name: "Daily cap" });
    await user.clear(capField);
    await user.type(capField, "10001");
    expect(section.getAllByRole("button", { name: "Save" })[1]).toBeDisabled();
    await user.clear(capField);
    await user.type(capField, "0");
    expect(section.getAllByRole("button", { name: "Save" })[1]).toBeEnabled();
  });

  it("stores only the tiers that differ from the defaults", async () => {
    stub({
      steward: [mode("live"), veto(), cap(), tiers('{"priority":"human"}')],
    });
    const update = vi
      .spyOn(apiClient, "updateSetting")
      .mockResolvedValue(tiers('{"priority":"human","ticket_ready":"veto"}'));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toHaveTextContent("human");
    await chooseFromDropdown(user, "Open a preparing ticket", "veto");

    expect(update).toHaveBeenCalledWith("steward_category_tiers", {
      type: "string",
      string_val: '{"priority":"human","ticket_ready":"veto"}',
    });
    await chooseFromDropdown(user, "Change a ticket's priority", "veto");
    expect(update).toHaveBeenLastCalledWith("steward_category_tiers", {
      type: "string",
      string_val: '{"ticket_ready":"veto"}',
    });
  });

  it("shows the server's refusal of a tier", async () => {
    stub({ steward: [mode("live"), veto(), cap(), tiers()] });
    vi.spyOn(apiClient, "updateSetting").mockRejectedValue(
      new ApiError(422, "steward: bookkeeping is never automatic"),
    );
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await chooseFromDropdown(user, "Blockers and child tickets from an approved plan", "human");

    expect(await section.findByRole("alert")).toHaveTextContent("never automatic");
  });

  it("holds the other categories while a tier write is pending, so edits cannot overwrite each other", async () => {
    stub({ steward: [mode("live"), veto(), cap(), tiers()] });
    let finish: (s: ReturnType<typeof tiers>) => void = () => {};
    const update = vi
      .spyOn(apiClient, "updateSetting")
      .mockImplementation(() => new Promise((resolve) => (finish = resolve)));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    await chooseFromDropdown(user, "Open a preparing ticket", "veto");
    expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toBeDisabled();
    finish(tiers('{"ticket_ready":"veto"}'));
    await vi.waitFor(() => expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toBeEnabled());
    expect(update).toHaveBeenCalledTimes(1);
  });

  describe("below level 3", () => {
    const below = (stored = "{}") => stub({ steward: [mode(), level(1), veto(), cap(), tiers(stored)] });

    it("disables the tier dropdowns and says tiers apply from level 3", async () => {
      below();
      await renderSignedIn(<TenantSettingsPage />);

      const section = await steward();
      expect(section.getByText(/Tiers apply from level 3 \(Routine\)\. At this level every decision is yours\./)).toBeInTheDocument();
      expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toBeDisabled();
      expect(section.getByRole("combobox", { name: "Open a preparing ticket" })).toBeDisabled();
      for (const c of STEWARD_CATEGORIES) expect(section.getByRole("combobox", { name: c.label })).toBeDisabled();
    });

    it("lists a stored override as applying from level 3, and clears it", async () => {
      below('{"priority":"veto","merge_continue":"auto"}');
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(tiers('{"merge_continue":"auto"}'));
      const user = userEvent.setup();
      await renderSignedIn(<TenantSettingsPage />);

      const section = await steward();
      expect(section.getByText("veto (applies from level 3)")).toBeInTheDocument();
      expect(section.getByText("auto (applies from level 3)")).toBeInTheDocument();
      await user.click(section.getByRole("button", { name: "Clear the stored tier for Change a ticket's priority" }));

      expect(update).toHaveBeenCalledWith("steward_category_tiers", {
        type: "string",
        string_val: '{"merge_continue":"auto"}',
      });
      await vi.waitFor(() => expect(section.queryByText("veto (applies from level 3)")).toBeNull());
    });

    it("names the stored overrides in the confirmation of a raise to level 3", async () => {
      below('{"priority":"veto"}');
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(3));
      const user = userEvent.setup();
      await renderSignedIn(<TenantSettingsPage />);

      const section = await steward();
      await user.click(section.getByRole("radio", { name: /3 Routine/ }));
      const dialog = section.getByRole("alertdialog");
      expect(dialog).toHaveTextContent("Change a ticket's priority (veto)");
      expect(update).not.toHaveBeenCalled();
      await user.click(within(dialog).getByRole("button", { name: "Save" }));
      expect(update).toHaveBeenCalledWith("steward_level", { type: "int", int_val: 3 });
    });

    it("raises to level 3 without asking when nothing is stored", async () => {
      below();
      const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(level(3));
      const user = userEvent.setup();
      await renderSignedIn(<TenantSettingsPage />);

      const section = await steward();
      await user.click(section.getByRole("radio", { name: /3 Routine/ }));
      expect(section.queryByRole("alertdialog")).toBeNull();
      expect(update).toHaveBeenCalledWith("steward_level", { type: "int", int_val: 3 });
    });
  });

  it("at level 3 enables the dropdowns, shows the stored tier and saves a choice", async () => {
    stub({ steward: [mode(), level(3), veto(), cap(), tiers('{"priority":"human"}')] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(tiers('{"priority":"human","ticket_ready":"veto"}'));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    expect(section.queryByText(/Tiers apply from level 3/)).toBeNull();
    expect(section.queryByText(/applies from level 3/)).toBeNull();
    expect(section.getByRole("combobox", { name: "Open a preparing ticket" })).toBeEnabled();
    await chooseFromDropdown(user, "Open a preparing ticket", "veto");
    expect(update).toHaveBeenCalled();
    await vi.waitFor(() => expect(section.getByRole("combobox", { name: "Open a preparing ticket" })).toHaveTextContent("veto"));
  });


  it("edits the review size limit and says generated files don't count", async () => {
    stub({ steward: [mode(), veto(), cap(), reviewMax(), tiers()] });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(reviewMax(250));
    const user = userEvent.setup();
    await renderSignedIn(<TenantSettingsPage />);

    const section = await steward();
    const field = await section.findByRole("spinbutton", { name: "Review size limit" });
    expect(field).toHaveValue(400);
    expect(section.getByText(/Generated files don't count/)).toBeInTheDocument();
    await user.clear(field);
    await user.type(field, "250");
    await user.click(section.getAllByRole("button", { name: "Save" })[2]);

    expect(update).toHaveBeenCalledWith("steward_review_max_lines", { type: "int", int_val: 250 });

    const again = await section.findByRole("spinbutton", { name: "Review size limit" });
    await user.clear(again);
    await user.type(again, "100001");
    expect(section.getAllByRole("button", { name: "Save" })[2]).toBeDisabled();
    await user.clear(again);
    await user.type(again, "0");
    expect(section.getAllByRole("button", { name: "Save" })[2]).toBeDisabled();
  });

  it("links to the steward instructions and the sensitive word list", async () => {
    stub();
    await renderSignedIn(<TenantSettingsPage />);
    const section = await steward();
    expect(section.getByRole("link", { name: "steward instructions" })).toHaveAttribute("href", "/workspace");
    expect(section.getByRole("link", { name: "sensitive word list" })).toHaveAttribute("href", "/workspace");
  });
});
