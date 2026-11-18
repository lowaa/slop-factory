import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeSetting, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import ReviewProviderSection from "./review_provider_section";
import TenantSettingsPage from "./tenant_settings_page";
import { REVIEW_PROVIDER_CONFIG } from "./review_provider_settings";

const setting = (string_val: string) =>
  makeSetting({ name: REVIEW_PROVIDER_CONFIG, type: "string", string_val, bool_val: null, is_default: string_val === "" });

const STORED = JSON.stringify({
  primary: { provider: "claude", model: "", reasoning_effort: "high" },
  fallback: { provider: "codex", model: "", reasoning_effort: "" },
  fallback_on: ["usage_limit"],
});

function stub({ isOwner = true, value = "" }: { isOwner?: boolean; value?: string } = {}) {
  vi.spyOn(apiClient, "listSettings").mockResolvedValue([setting(value)]);
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: isOwner })]);
  vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([]);
}

async function section() {
  await renderSignedIn(<ReviewProviderSection />);
  return within(await screen.findByRole("region", { name: "Review provider" }));
}

describe("the Review provider section", () => {
  it("says it is loading until the settings arrive", async () => {
    vi.spyOn(apiClient, "listSettings").mockReturnValue(new Promise(() => {}));
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: true })]);
    await renderSignedIn(<ReviewProviderSection />);
    expect(within(await screen.findByRole("region", { name: "Review provider" })).getByText("Loading…")).toBeInTheDocument();
  });

  it("shows nothing of its own when the settings cannot be read: the page reports that once", async () => {
    vi.spyOn(apiClient, "listSettings").mockRejectedValue(new ApiError(500, "settings are down"));
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: true })]);
    const s = await section();
    await vi.waitFor(() => expect(s.queryByText("Loading…")).toBeNull());
    expect(s.queryByRole("alert")).toBeNull();
    expect(s.queryByRole("combobox")).toBeNull();
  });

  it("offers an empty form when none is set, with Save off and no Clear", async () => {
    stub();
    const s = await section();
    expect(await s.findByRole("combobox", { name: "Primary provider" })).toHaveTextContent("Choose…");
    expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(s.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  it("saves the chosen primary as the stored JSON and shows it as stored", async () => {
    const user = userEvent.setup();
    stub();
    const stored = JSON.stringify({ primary: { provider: "claude", model: "claude-sonnet-5-5", reasoning_effort: "high" } });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(setting(stored));
    const s = await section();
    await s.findByRole("combobox", { name: "Primary provider" });

    await chooseFromDropdown(user, "Primary provider", "claude");
    await user.type(s.getByLabelText(/Primary model/), " claude-sonnet-5-5 ");
    await chooseFromDropdown(user, "Primary reasoning effort", "high");
    await user.click(s.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalledWith(REVIEW_PROVIDER_CONFIG, {
      type: "string",
      string_val: JSON.stringify({ primary: { provider: "claude", model: "claude-sonnet-5-5", reasoning_effort: "high" } }),
    });
    expect(await s.findByRole("button", { name: "Clear" })).toBeInTheDocument();
    expect(s.queryByText(/could not be read/)).toBeNull();
    expect(selectedValueOf("Primary reasoning effort")).toBe("high");
    expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("refuses a primary changed to the provider the fallback already uses", async () => {
    const user = userEvent.setup();
    stub({ value: STORED });
    const s = await section();
    await chooseFromDropdown(user, "Primary provider", "codex");
    expect(s.getByText(/fallback provider must differ/)).toBeInTheDocument();
    expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("saves a fallback with the failures it is taken on", async () => {
    const user = userEvent.setup();
    stub();
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(setting("saved"));
    const s = await section();
    await chooseFromDropdown(user, "Primary provider", "claude");
    await user.click(s.getByRole("checkbox", { name: "Use a fallback provider" }));
    await chooseFromDropdown(user, "Fallback provider", "openai");
    await user.click(s.getByRole("checkbox", { name: "Backend error" }));
    await user.click(s.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalledWith(REVIEW_PROVIDER_CONFIG, {
      type: "string",
      string_val: JSON.stringify({
        primary: { provider: "claude", model: "", reasoning_effort: "" },
        fallback: { provider: "openai", model: "", reasoning_effort: "" },
        fallback_on: ["usage_limit", "backend"],
      }),
    });
  });

  it("shows the stored choice, and Save stays off until something changes", async () => {
    stub({ value: STORED });
    const s = await section();
    expect(await s.findByRole("combobox", { name: "Primary provider" })).toHaveTextContent("claude");
    expect(selectedValueOf("Primary reasoning effort")).toBe("high");
    expect(selectedValueOf("Fallback provider")).toBe("codex");
    expect(selectedValueOf("Fallback reasoning effort")).toBe("Default (xhigh)");
    expect(s.getByRole("checkbox", { name: "Usage limit" })).toBeChecked();
    expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("offers no reasoning effort for a provider that takes none", async () => {
    const user = userEvent.setup();
    stub();
    const s = await section();
    await chooseFromDropdown(user, "Primary provider", "openrouter");
    expect(s.getByRole("combobox", { name: "Primary reasoning effort" })).toBeDisabled();
    expect(s.getByText(/selects effort by model name/)).toBeInTheDocument();
  });

  it("drops an effort the newly chosen provider does not take", async () => {
    const user = userEvent.setup();
    stub({ value: STORED });
    await section();
    await chooseFromDropdown(user, "Primary provider", "openai");
    expect(selectedValueOf("Primary reasoning effort")).toBe("Model default");
  });

  it("refuses, beside the form and without a request, a bad model, a repeated provider and no trigger", async () => {
    const user = userEvent.setup();
    stub();
    const update = vi.spyOn(apiClient, "updateSetting");
    const s = await section();
    await chooseFromDropdown(user, "Primary provider", "claude");
    await user.type(s.getByLabelText(/Primary model/), "bad model!");
    await user.click(s.getByRole("checkbox", { name: "Use a fallback provider" }));
    await user.click(s.getByRole("checkbox", { name: "Usage limit" }));

    const problems = within(s.getByRole("list", { name: "Problems" }));
    expect(problems.getByText(/primary model must be a model slug/)).toBeInTheDocument();
    expect(problems.getByText(/at least one failure/)).toBeInTheDocument();
    expect(s.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(update).not.toHaveBeenCalled();
    await user.click(s.getByRole("combobox", { name: "Fallback provider" }));
    expect(await screen.findByRole("option", { name: "claude" })).toHaveAttribute("aria-disabled", "true");
  });

  it("shows the server's refusal inline and keeps the draft", async () => {
    const user = userEvent.setup();
    stub();
    vi.spyOn(apiClient, "updateSetting").mockRejectedValue(new ApiError(422, "primary.provider \"claude\" is not available"));
    const s = await section();
    await chooseFromDropdown(user, "Primary provider", "claude");
    await user.click(s.getByRole("button", { name: "Save" }));

    expect(await s.findByRole("alert")).toHaveTextContent('primary.provider "claude" is not available');
    expect(s.getByRole("combobox", { name: "Primary provider" })).toHaveTextContent("claude");
  });

  it("clears back to none set by storing an empty string", async () => {
    const user = userEvent.setup();
    stub({ value: STORED });
    const update = vi.spyOn(apiClient, "updateSetting").mockResolvedValue(setting(""));
    const s = await section();
    await user.click(await s.findByRole("button", { name: "Clear" }));

    expect(update).toHaveBeenCalledWith(REVIEW_PROVIDER_CONFIG, { type: "string", string_val: "" });
    expect(await s.findByRole("combobox", { name: "Primary provider" })).toHaveTextContent("Choose…");
    expect(s.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  it("shows a member the choice read-only", async () => {
    stub({ isOwner: false, value: STORED });
    const s = await section();
    expect(await s.findByText("Primary: claude, default model, high effort")).toBeInTheDocument();
    expect(s.getByText("Fallback: codex, default model, on usage_limit")).toBeInTheDocument();
    expect(s.getByText("Only an owner of the tenant can change this.")).toBeInTheDocument();
    expect(s.queryByRole("combobox")).toBeNull();
    expect(s.queryByRole("button")).toBeNull();
  });

  it("tells a member when none is set", async () => {
    stub({ isOwner: false });
    const s = await section();
    expect(await s.findByText(/None set: runs use each machine's local settings/)).toBeInTheDocument();
  });

  it("is on the settings page, and not repeated in the generic list", async () => {
    stub({ value: STORED });
    await renderSignedIn(<TenantSettingsPage />);
    await screen.findByRole("region", { name: "Review provider" });
    expect(screen.queryByText(REVIEW_PROVIDER_CONFIG)).toBeNull();
    expect(screen.getAllByText("Review provider")).toHaveLength(1);
  });
});
