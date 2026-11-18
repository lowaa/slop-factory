import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw/http";
import { setupServer } from "msw/node";
import { ServerStub, decidedState, freshState, newStore, wirePurpose, wireState } from "./test/fixtures";
import { renderWithConsent } from "./test/render";
import CookiePreferencesButton from "./cookie_preferences_button";
import ConsentSettings from "./consent_settings";

const stub = new ServerStub();
const server = setupServer(...stub.handlers);

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => {
  server.resetHandlers();
  stub.state = freshState();
  stub.nextPut = null;
  stub.puts = [];
});
afterAll(() => server.close());

const dialog = () => screen.queryByRole("dialog", { name: "Cookie preferences" });

describe("the preferences dialog", () => {
  it("lists every cookie purpose with its description, necessary ones always on", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    const d = dialog()!;
    const necessary = within(d).getByRole("switch", { name: "Strictly necessary" });
    expect(necessary).toBeChecked();
    expect(necessary).toBeDisabled();
    expect(necessary).toHaveAccessibleDescription(/Always on/);
    const analytics = within(d).getByRole("switch", { name: "Analytics" });
    expect(analytics).not.toBeChecked();
    expect(analytics).toHaveAccessibleDescription(/which features are used/);
  });

  it("saves exactly the toggles the person set", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    await userEvent.click(screen.getByRole("switch", { name: "Analytics" }));
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: true, marketing: false }));
    await userEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(dialog()).not.toBeInTheDocument());
    expect(stub.puts[0]).toMatchObject({
      source: "settings",
      decisions: [
        expect.objectContaining({ purpose: "analytics", granted: true }),
        expect.objectContaining({ purpose: "marketing", granted: false }),
      ],
    });
  });

  it("withdraws a grant in the same two steps it was given", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    const analytics = screen.getByRole("switch", { name: "Analytics" });
    expect(analytics).toBeChecked();
    await userEvent.click(analytics);
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false, marketing: false }));
    await userEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(dialog()).not.toBeInTheDocument());
    expect(stub.puts[0]?.decisions).toEqual([expect.objectContaining({ purpose: "analytics", granted: false })]);
  });

  it("sends nothing when nothing was changed", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    await userEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    expect(stub.puts).toHaveLength(0);
    expect(dialog()).not.toBeInTheDocument();
  });

  it("records a decision made through the banner's Manage as coming from the banner", async () => {
    await renderWithConsent(newStore());
    await userEvent.click(screen.getByRole("button", { name: "Manage preferences" }));
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false, marketing: false }));
    await userEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(stub.puts).toHaveLength(1));
    expect(stub.puts[0]?.source).toBe("banner");
  });

  it("offers Reject all and Accept all at the same prominence", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    const d = within(dialog()!);
    expect(d.getByRole("button", { name: "Reject all" }).className).toBe(
      d.getByRole("button", { name: "Accept all" }).className,
    );
  });

  it("leaves a processing purpose to its own flow", async () => {
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics"),
      wirePurpose("health_data_processing", { kind: "processing" }),
    ]);
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    expect(screen.queryByRole("switch", { name: /health_data_processing/ })).not.toBeInTheDocument();
  });
});

describe("Global Privacy Control in the dialog", () => {
  it("shows the signal honoured and the denied purpose off and unchangeable", async () => {
    stub.state = wireState(
      [wirePurpose("necessary"), wirePurpose("analytics"), wirePurpose("marketing", { gpc_locked: true })],
      { gpc: true },
    );
    await renderWithConsent(newStore({ gpcSignal: () => true }), <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    const d = within(dialog()!);
    expect(d.getByTestId("gpc-notice")).toBeInTheDocument();
    const marketing = d.getByRole("switch", { name: "Marketing" });
    expect(marketing).toBeDisabled();
    expect(marketing).not.toBeChecked();
    expect(marketing).toHaveAccessibleDescription(/Global Privacy Control/);
  });
});

describe("a decision made elsewhere while the dialog is open", () => {
  it("is shown, and not overwritten by saving", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    const store = newStore();
    await renderWithConsent(store, <CookiePreferencesButton />);
    await userEvent.click(screen.getByRole("button", { name: "Cookie preferences" }));
    expect(screen.getByRole("switch", { name: "Analytics" })).toBeChecked();
    stub.state = decidedState({ analytics: false, marketing: false });
    await act(() => store.refresh());
    expect(screen.getByRole("switch", { name: "Analytics" })).not.toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    expect(stub.puts).toHaveLength(0);
  });
});

describe("focus and keyboard", () => {
  it("moves focus into the dialog, keeps Tab inside it, and gives focus back on Escape", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    const opener = screen.getByRole("button", { name: "Cookie preferences" });
    await userEvent.click(opener);
    const d = dialog()!;
    expect(d).toHaveFocus();
    expect(d).toHaveAttribute("aria-modal", "true");

    const close = within(d).getByRole("button", { name: "Close" });
    close.focus();
    await userEvent.tab();
    expect(d.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(close);
    await userEvent.tab({ shift: true });
    expect(document.activeElement).toBe(close);

    await userEvent.keyboard("{Escape}");
    expect(dialog()).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("can be reached and operated by keyboard alone", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    screen.getByRole("button", { name: "Cookie preferences" }).focus();
    await userEvent.keyboard("{Enter}");
    const analytics = screen.getByRole("switch", { name: "Analytics" });
    analytics.focus();
    await userEvent.keyboard(" ");
    expect(analytics).toBeChecked();
  });
});

describe("the settings entry point", () => {
  it("shows what is on and off, and opens the dialog", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    await renderWithConsent(newStore(), <ConsentSettings />);
    const section = within(screen.getByRole("region", { name: "Cookie preferences" }));
    expect(section.getByText("Analytics:")).toBeInTheDocument();
    expect(section.getByText("On")).toBeInTheDocument();
    expect(section.getByText("Off")).toBeInTheDocument();
    await userEvent.click(section.getByRole("button", { name: "Change cookie preferences" }));
    expect(dialog()).toBeInTheDocument();
  });
});
