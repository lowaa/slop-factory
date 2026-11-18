import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type IntakeSources } from "@/api";
import { makeIntakeDelivery, makeIntakeSource } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import IntakeSourcesPanel from "./intake_sources_panel";
import { FIELD_DOCS, FIELD_KEYS } from "./intake_field_docs";

function stubSources(overrides: Partial<IntakeSources> = {}) {
  return vi.spyOn(apiClient, "listIntakeSources").mockResolvedValue({
    enabled: true,
    hmac: true,
    sources: [],
    deliveries: [],
    ...overrides,
  });
}

function stubReauth() {
  vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
    password: { email: "person@example.com", verified: true },
    identities: [],
    password_login_enabled: true,
    reauth_required_after: "2026-01-01T00:00:00Z",
  });
  return vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);
}

// The form's selects are Dropdowns: open the combobox, click the option.
async function choose(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
  option: string,
) {
  await user.click(screen.getByRole("combobox", { name: label }));
  await user.click(screen.getByRole("option", { name: option }));
}

async function confirmPassword(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText("Confirm it’s you");
  await user.type(screen.getByLabelText("Current password"), "correct-password");
  await user.click(screen.getByRole("button", { name: "Confirm" }));
}

describe("IntakeSourcesPanel", () => {
  it("credits a source made by a deleted account to 'Deleted user'", async () => {
    stubSources({ sources: [makeIntakeSource({ created_by: null, created_by_deleted: true })] });

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(await screen.findByText("Deleted user")).toBeInTheDocument();
  });

  it("lists sources and deliveries, naming the source by id lookup", async () => {
    stubSources({
      sources: [
        makeIntakeSource({
          id: "s1",
          name: "prom-alerts",
          adapter: "alertmanager",
          url: "https://board.example/intake/s1",
        }),
      ],
      deliveries: [
        makeIntakeDelivery({
          id: "d1",
          source_id: "s1",
          event: "open",
          key: "disk-full",
          outcome: "applied",
          detail: "opened ticket #7",
        }),
        makeIntakeDelivery({ id: "d2", source_id: "gone", key: "other-key", detail: "other" }),
      ],
    });

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(
      await screen.findByText("https://board.example/intake/s1"),
    ).toBeInTheDocument();
    expect(screen.getAllByText("prom-alerts").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("alertmanager", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("last delivery never")).toBeInTheDocument();
    expect(screen.getByText("disk-full")).toBeInTheDocument();
    expect(screen.getByText("opened ticket #7")).toBeInTheDocument();
    expect(screen.getByText("gone")).toBeInTheDocument();
  });

  it("says so when there are no sources or deliveries", async () => {
    stubSources();

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(await screen.findByText("No sources yet.")).toBeInTheDocument();
    expect(screen.getByText("No deliveries yet.")).toBeInTheDocument();
  });

  it("disables create and shows a notice when the intake door isn't configured", async () => {
    stubSources({ enabled: false });

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(
      await screen.findByText(
        "The intake door is not configured on this server.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Create source" }),
    ).toBeDisabled();
  });

  it("disables the hmac option when the server has no INTAKE_KEY", async () => {
    stubSources({ hmac: false });

    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.click(screen.getByRole("combobox", { name: "Credential type" }));
    expect(screen.getByRole("option", { name: "hmac" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("option", { name: "bearer" })).not.toHaveAttribute("aria-disabled", "true");

    await user.click(screen.getByRole("option", { name: "hmac" }));
    expect(screen.getByRole("combobox", { name: "Credential type" })).toHaveTextContent("bearer");
  });

  it("forces the default status to preparing for an untrusted source", async () => {
    stubSources();
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    const status = screen.getByLabelText("Default status");
    expect(status).toHaveTextContent("preparing");
    expect(status).toBeDisabled();

    await choose(user, "Trust", "trusted");
    expect(status).toBeEnabled();
    await choose(user, "Default status", "open");
    expect(status).toHaveTextContent("open");

    await choose(user, "Trust", "untrusted");
    expect(status).toHaveTextContent("preparing");
    expect(status).toBeDisabled();
  });

  it("creates a bearer source, shows its token and URL once, and Done clears them", async () => {
    const list = stubSources();
    const create = vi.spyOn(apiClient, "createIntakeSource").mockResolvedValue({
      source: makeIntakeSource({
        id: "s1",
        url: "https://board.example/intake/s1",
      }),
      token: "tok-123",
    });
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    list.mockResolvedValue({
      enabled: true,
      hmac: true,
      sources: [makeIntakeSource({ id: "s1" })],
      deliveries: [],
    });
    await user.type(screen.getByLabelText("Name"), "alerts");
    await user.type(
      screen.getByLabelText("Roles"),
      "ops, db",
    );
    await user.type(screen.getByLabelText("Paths"), "infra/");
    await choose(user, "Role match", "all");
    await user.click(screen.getByRole("button", { name: "Create source" }));

    expect(create).toHaveBeenCalledWith({
      name: "alerts",
      adapter: "native",
      trust: "untrusted",
      auth: "bearer",
      default_status: "preparing",
      default_priority: "normal",
      max_priority: "high",
      reopen_policy: "new",
      resolve_closes: false,
      roles: ["ops", "db"],
      paths: ["infra/"],
      role_match: "all",
    });
    const group = await screen.findByRole("group", { name: "New credential" });
    expect(
      within(group).getByText("https://board.example/intake/s1"),
    ).toBeInTheDocument();
    expect(
      within(group).getByText("Authorization: Bearer tok-123"),
    ).toBeInTheDocument();
    expect(within(group).getByText(/shown only once/)).toBeInTheDocument();

    await user.click(within(group).getByRole("button", { name: "Done" }));

    expect(
      screen.queryByText("Authorization: Bearer tok-123"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("group", { name: "New credential" }),
    ).not.toBeInTheDocument();
  });

  it("shows the HMAC secret and signature header for an hmac source", async () => {
    stubSources();
    vi.spyOn(apiClient, "createIntakeSource").mockResolvedValue({
      source: makeIntakeSource({ auth: "hmac" }),
      secret: "hmac-secret-value",
      signature_header: "X-Slop-Signature",
    });
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.type(screen.getByLabelText("Name"), "desk");
    await choose(user, "Credential type", "hmac");
    await user.click(screen.getByRole("button", { name: "Create source" }));

    expect(await screen.findByText("hmac-secret-value")).toBeInTheDocument();
    expect(screen.getByText("X-Slop-Signature")).toBeInTheDocument();
    expect(screen.queryByText(/Authorization: Bearer/)).not.toBeInTheDocument();
  });

  it("shows the server's validation message when create fails", async () => {
    stubSources();
    vi.spyOn(apiClient, "createIntakeSource").mockRejectedValue(
      new ApiError(422, "name already in use"),
    );
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.type(screen.getByLabelText("Name"), "alerts");
    await user.click(screen.getByRole("button", { name: "Create source" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "name already in use",
    );
  });

  it("steps up on reauth_required for create, keeping the form values for the retry", async () => {
    stubSources();
    const create = vi
      .spyOn(apiClient, "createIntakeSource")
      .mockRejectedValueOnce(
        new ApiError(403, "confirm it's you", "reauth_required"),
      )
      .mockResolvedValueOnce({
        source: makeIntakeSource(),
        token: "post-reauth-token",
      });
    const reauth = stubReauth();
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.type(screen.getByLabelText("Name"), "kept-name");
    await user.click(screen.getByRole("button", { name: "Create source" }));
    await confirmPassword(user);

    expect(reauth).toHaveBeenCalledWith("correct-password");
    expect(
      await screen.findByText("Authorization: Bearer post-reauth-token"),
    ).toBeInTheDocument();
    expect(create).toHaveBeenCalledTimes(2);
    expect(create.mock.calls[1][0]).toEqual(
      expect.objectContaining({ name: "kept-name" }),
    );
    expect(screen.queryByText("Confirm it’s you")).not.toBeInTheDocument();
  });

  it("cancelling the step-up prompt brings the form back with its values", async () => {
    stubSources();
    vi.spyOn(apiClient, "createIntakeSource").mockRejectedValue(
      new ApiError(403, "confirm it's you", "reauth_required"),
    );
    stubReauth();
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.type(screen.getByLabelText("Name"), "kept-name");
    await user.click(screen.getByRole("button", { name: "Create source" }));
    await screen.findByText("Confirm it’s you");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByText("Confirm it’s you")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("kept-name");
  });

  it("rotates a source through step-up and shows the new credential", async () => {
    stubSources({ sources: [makeIntakeSource({ id: "s1" })] });
    const rotate = vi
      .spyOn(apiClient, "rotateIntakeSource")
      .mockRejectedValueOnce(
        new ApiError(403, "confirm it's you", "reauth_required"),
      )
      .mockResolvedValueOnce({
        source: makeIntakeSource({ id: "s1" }),
        token: "rotated-token",
      });
    stubReauth();
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByRole("button", { name: "Rotate" });

    await user.click(screen.getByRole("button", { name: "Rotate" }));
    await confirmPassword(user);

    expect(
      await screen.findByText("Authorization: Bearer rotated-token"),
    ).toBeInTheDocument();
    expect(rotate).toHaveBeenCalledTimes(2);
    expect(rotate).toHaveBeenLastCalledWith("s1");
  });

  it("revokes a source and refreshes the list", async () => {
    const list = stubSources({ sources: [makeIntakeSource({ id: "s1" })] });
    const revoke = vi
      .spyOn(apiClient, "revokeIntakeSource")
      .mockResolvedValue(undefined);
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByRole("button", { name: "Revoke" });

    list.mockResolvedValue({
      enabled: true,
      hmac: true,
      sources: [
        makeIntakeSource({ id: "s1", revoked_at: "2026-01-02T00:00:00Z" }),
      ],
      deliveries: [],
    });
    await user.click(screen.getByRole("button", { name: "Revoke" }));

    expect(revoke).toHaveBeenCalledWith("s1");
    expect(await screen.findByText("Revoked")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Revoke" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Rotate" }),
    ).not.toBeInTheDocument();
  });

  it("shows the server's error when revoking fails", async () => {
    stubSources({ sources: [makeIntakeSource({ id: "s1" })] });
    vi.spyOn(apiClient, "revokeIntakeSource").mockRejectedValue(
      new ApiError(404, "not found"),
    );
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByRole("button", { name: "Revoke" });

    await user.click(screen.getByRole("button", { name: "Revoke" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not found");
  });

  it("badges a delivery that was downgraded to preparing, and only that one", async () => {
    stubSources({
      deliveries: [
        makeIntakeDelivery({ id: "d1", downgraded: true }),
        makeIntakeDelivery({ id: "d2", downgraded: false }),
      ],
    });

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(await screen.findAllByText("downgraded to preparing")).toHaveLength(
      1,
    );
  });

  it("links a delivery's ticket only when it names one", async () => {
    stubSources({
      deliveries: [
        makeIntakeDelivery({ id: "d1", ticket_id: 42 }),
        makeIntakeDelivery({ id: "d2", ticket_id: null }),
      ],
    });

    await renderSignedIn(<IntakeSourcesPanel />);

    const link = await screen.findByRole("link", { name: "#42" });
    expect(link).toHaveAttribute(
      "href",
      expect.stringContaining("/tickets/42"),
    );
    expect(link).toHaveAttribute("href", expect.stringContaining("from=intake"));
    expect(screen.getAllByRole("link", { name: /^#\d+$/ })).toHaveLength(1);
  });

  it("renders sender-controlled key and detail as literal text", async () => {
    stubSources({
      deliveries: [
        makeIntakeDelivery({
          id: "d1",
          key: "<img src=x onerror=alert(1)>",
          detail: "<script>alert(2)</script> [link](https://evil.example)",
        }),
      ],
    });

    const { container } = await renderSignedIn(<IntakeSourcesPanel />);

    expect(
      await screen.findByText("<img src=x onerror=alert(1)>"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "<script>alert(2)</script> [link](https://evil.example)",
      ),
    ).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector('a[href*="evil"]')).toBeNull();
  });

  it("says so when the sources cannot be loaded", async () => {
    vi.spyOn(apiClient, "listIntakeSources").mockRejectedValue(new ApiError(500, "the sources are gone", "internal"));

    await renderSignedIn(<IntakeSourcesPanel />);

    expect(await screen.findByRole("alert")).toHaveTextContent("the sources are gone");
  });

  // The mount fetch and the refresh after a create can be in flight together;
  // without the issued/applied guard, whichever resolves last wins, even the
  // older one -- as the members panel's test of the same guard shows.
  it("keeps the newer refresh even when an older, slower one resolves after it", async () => {
    let resolveMount: (sources: IntakeSources) => void = () => {};
    const mountFetch = new Promise<IntakeSources>((resolve) => {
      resolveMount = resolve;
    });
    const list = vi.spyOn(apiClient, "listIntakeSources").mockReturnValueOnce(mountFetch);
    vi.spyOn(apiClient, "createIntakeSource").mockResolvedValue({
      source: makeIntakeSource({ id: "s-new", name: "brand-new" }),
      token: "shi_x",
    });
    list.mockResolvedValueOnce({
      enabled: true,
      hmac: true,
      sources: [makeIntakeSource({ id: "s-new", name: "brand-new" })],
      deliveries: [],
    });
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("Loading…");

    await user.type(screen.getByLabelText("Name"), "brand-new");
    await user.click(screen.getByRole("button", { name: "Create source" }));
    await screen.findByRole("group", { name: "New credential" });
    await screen.findByText("brand-new", { selector: "li *, li" });

    resolveMount({ enabled: true, hmac: true, sources: [], deliveries: [] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByText("brand-new", { selector: "li *, li" })).toBeInTheDocument();
  });
  describe("field help", () => {
    // Trusted and with roles set, so no control is disabled and the role
    // match field is on screen: every field the form can show is showing.
    async function showEveryField(user: ReturnType<typeof userEvent.setup>) {
      stubSources();
      await renderSignedIn(<IntakeSourcesPanel />);
      await screen.findByText("No sources yet.");
      await choose(user, "Trust", "trusted");
      await user.type(screen.getByLabelText("Roles"), "ops");
    }

    it("shows every documented field, each with its visible hint", async () => {
      const user = userEvent.setup();
      await showEveryField(user);

      for (const key of FIELD_KEYS) {
        const doc = FIELD_DOCS[key];
        expect(screen.getByLabelText(doc.label), key).toBeInTheDocument();
        expect(screen.getByText(doc.hint), key).toBeInTheDocument();
      }
    });

    // Walks the controls actually rendered, not the doc table, so a field
    // added to the form without a tooltip fails here.
    it("gives every control in the form a tooltip that describes it", async () => {
      const user = userEvent.setup();
      await showEveryField(user);

      const form = screen.getByRole("form", { name: "New intake source" });
      const controls = Array.from(form.querySelectorAll("input, [role=combobox]"));
      expect(controls).toHaveLength(FIELD_KEYS.length);

      for (const control of controls) {
        await user.hover(control);
        const tooltip = await screen.findByRole("tooltip");
        expect(tooltip.textContent, control.id).not.toBe("");
        expect(control.getAttribute("aria-describedby")).toBe(tooltip.id);
        const doc = FIELD_KEYS.map((k) => FIELD_DOCS[k]).find(
          (d) => d.tooltip === tooltip.textContent,
        );
        expect(screen.getByLabelText(doc?.label ?? "<no doc>")).toBe(control);
        await user.unhover(control);
        expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
      }
    });

    it("opens a field's tooltip on keyboard focus too", async () => {
      const user = userEvent.setup();
      await showEveryField(user);

      screen.getByLabelText("Name").focus();
      await user.tab();
      expect(await screen.findByRole("tooltip")).toHaveTextContent(
        FIELD_DOCS.trust.tooltip,
      );
    });
  });

  it("says any agent and none for a source with no roles or paths", async () => {
    stubSources({ sources: [makeIntakeSource({ name: "plain", roles: [], paths: [] })] });
    await renderSignedIn(<IntakeSourcesPanel />);

    const card = (await screen.findByRole("heading", { name: "plain" })).closest("li")!;
    expect(within(card).getByText("any agent")).toBeInTheDocument();
    expect(within(card).getByText("none")).toBeInTheDocument();
  });

  it("sends role match any when no roles are set, whatever the select was left on", async () => {
    stubSources({ hmac: true });
    const create = vi.spyOn(apiClient, "createIntakeSource").mockResolvedValue({
      source: makeIntakeSource({ id: "s1" }),
      token: "placeholder-token",
    });
    const user = userEvent.setup();
    await renderSignedIn(<IntakeSourcesPanel />);
    await screen.findByText("No sources yet.");

    await user.type(screen.getByLabelText("Name"), "alerts");
    await user.type(screen.getByLabelText("Roles"), "ops");
    await choose(user, "Role match", "all");
    await user.clear(screen.getByLabelText("Roles"));
    await user.click(screen.getByRole("button", { name: "Create source" }));

    expect(create).toHaveBeenCalledWith(expect.objectContaining({ roles: [], role_match: "any" }));
  });

  it("groups a source's settings into sections on its card", async () => {
    stubSources({
      sources: [
        makeIntakeSource({
          id: "s1",
          name: "desk",
          trust: "trusted",
          auth: "hmac",
          adapter: "alertmanager",
          default_status: "open",
          default_priority: "low",
          max_priority: "urgent",
          roles: ["ops", "db"],
          role_match: "all",
          paths: ["infra/", "docs/"],
          reopen_policy: "reopen",
          resolve_closes: true,
        }),
      ],
    });
    await renderSignedIn(<IntakeSourcesPanel />);

    const card = (await screen.findByRole("heading", { name: "desk" })).closest("li")!;
    // Every setting under the group it belongs to, label and value both.
    const expected: Record<string, [string, string][]> = {
      Authentication: [[FIELD_DOCS.auth.label, "hmac"]],
      Mapping: [
        [FIELD_DOCS.adapter.label, "alertmanager"],
        [FIELD_DOCS.reopen_policy.label, "reopen the ticket"],
        [FIELD_DOCS.resolve_closes.label, "cancel it if unclaimed"],
      ],
      "Where tickets land": [
        [FIELD_DOCS.default_status.label, "open"],
        [FIELD_DOCS.default_priority.label, "low"],
        [FIELD_DOCS.max_priority.label, "urgent"],
        [FIELD_DOCS.roles.label, "ops, db (all)"],
        [FIELD_DOCS.paths.label, "infra/, docs/"],
      ],
    };
    for (const [title, settings] of Object.entries(expected)) {
      const group = within(card).getByRole("group", { name: title });
      const terms = within(group).getAllByRole("term").map((t) => t.textContent);
      const values = within(group).getAllByRole("definition").map((d) => d.textContent);
      expect(terms, title).toEqual(settings.map(([label]) => label));
      expect(values, title).toEqual(settings.map(([, value]) => value));
    }
    expect(within(card).getAllByRole("group")).toHaveLength(3);
  });

});
