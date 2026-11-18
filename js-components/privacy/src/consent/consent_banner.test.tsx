import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw/http";
import { setupServer } from "msw/node";
import { EQUAL_ACTION_PROPS } from "./equal_actions";
import {
  BASE,
  ServerStub,
  decidedState,
  freshState,
  newStore,
  wirePurpose,
  wireState,
} from "./test/fixtures";
import { renderWithConsent } from "./test/render";
import CookiePreferencesButton from "./cookie_preferences_button";

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

const banner = () => screen.queryByRole("region", { name: "Your privacy choices" });

describe("when the banner shows", () => {
  it("shows while a non-necessary purpose needs a decision", async () => {
    await renderWithConsent(newStore());
    expect(banner()).toBeInTheDocument();
  });

  it("does not show before the server has answered", async () => {
    let answer: () => void = () => undefined;
    const held = new Promise<void>((r) => (answer = r));
    server.use(
      http.get(BASE, async () => {
        await held;
        return HttpResponse.json(freshState());
      }),
    );
    const store = newStore();
    const pending = renderWithConsent(store);
    await Promise.resolve();
    expect(banner()).not.toBeInTheDocument();
    answer();
    await pending;
    await waitFor(() => expect(banner()).toBeInTheDocument());
  });

  it("does not show once every purpose has been decided", async () => {
    stub.state = decidedState({ analytics: false, marketing: false });
    await renderWithConsent(newStore());
    expect(banner()).not.toBeInTheDocument();
  });

  it("does not show for purposes that are only necessary, or processing consents", async () => {
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("health_data_processing", { kind: "processing" }),
    ]);
    await renderWithConsent(newStore());
    expect(banner()).not.toBeInTheDocument();
  });

  it("does not offer a purpose the server doesn't report (a capability flag that is off)", async () => {
    await renderWithConsent(newStore());
    await userEvent.click(screen.getByRole("button", { name: "Manage preferences" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByRole("switch", { name: "Interaction recording" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("switch", { name: "Analytics" })).toBeInTheDocument();
  });

  it("shows again when a purpose is added or its document changes", async () => {
    stub.state = decidedState({ analytics: false });
    const store = newStore();
    await renderWithConsent(store);
    expect(banner()).not.toBeInTheDocument();
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics", { needs_decision: false }),
      wirePurpose("interaction_recording"),
    ]);
    await store.load();
    await waitFor(() => expect(banner()).toBeInTheDocument());
  });

  it("shows again when a decided purpose's document has a new material version", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    const store = newStore();
    await renderWithConsent(store);
    expect(banner()).not.toBeInTheDocument();
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics", { granted: false, needs_decision: true, document_version: "2026-11-01" }),
      wirePurpose("marketing", { needs_decision: false }),
    ]);
    await store.load();
    await waitFor(() => expect(banner()).toBeInTheDocument());
  });
});

describe("what the banner offers", () => {
  it("makes Reject all exactly as prominent as Accept all", async () => {
    await renderWithConsent(newStore());
    const reject = screen.getByRole("button", { name: "Reject all" });
    const accept = screen.getByRole("button", { name: "Accept all" });
    expect(reject.className).toBe(accept.className);
    expect(reject).toHaveClass(`button--${EQUAL_ACTION_PROPS.variant}`, `button--${EQUAL_ACTION_PROPS.size}`);
    expect(reject.parentElement).toBe(accept.parentElement);
  });

  it("offers Manage, and links to the cookie policy", async () => {
    await renderWithConsent(newStore());
    expect(screen.getByRole("button", { name: "Manage preferences" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /cookie policy/i })).toHaveAttribute("href", "/legal/cookie_policy");
  });

  it("does not block the app: it is not a dialog and nothing sits over the page", async () => {
    await renderWithConsent(newStore(), <button>App button</button>);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.querySelector("[aria-modal]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "App button" }));
  });

  it("has nothing pre-ticked: the preferences it opens start with everything optional off", async () => {
    await renderWithConsent(newStore());
    await userEvent.click(screen.getByRole("button", { name: "Manage preferences" }));
    for (const sw of screen.getAllByRole("switch")) {
      const necessary = (sw as HTMLInputElement).disabled && (sw as HTMLInputElement).checked;
      if (!necessary) expect(sw).not.toBeChecked();
    }
  });
});

describe("deciding", () => {
  it("Reject all denies every optional purpose on the server, then the banner goes", async () => {
    const store = newStore();
    await renderWithConsent(store);
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false, marketing: false }));
    await userEvent.click(screen.getByRole("button", { name: "Reject all" }));
    await waitFor(() => expect(banner()).not.toBeInTheDocument());
    expect(stub.puts[0]?.decisions.map((d) => [d.purpose, d.granted])).toEqual([
      ["analytics", false],
      ["marketing", false],
    ]);
    expect(screen.getByTestId("consent-saved-announcer")).toHaveTextContent("saved");
  });

  it("Accept all grants them", async () => {
    const store = newStore();
    await renderWithConsent(store);
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: true, marketing: true }));
    await userEvent.click(screen.getByRole("button", { name: "Accept all" }));
    await waitFor(() => expect(store.isGranted("marketing")).toBe(true));
    expect(stub.puts[0]?.decisions.every((d) => d.granted)).toBe(true);
  });

  it("keeps the banner, and says so, when the choice couldn't be saved", async () => {
    await renderWithConsent(newStore());
    stub.nextPut = () => new HttpResponse(null, { status: 500 });
    await userEvent.click(screen.getByRole("button", { name: "Reject all" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("couldn't be saved");
    expect(banner()).toBeInTheDocument();
  });

  it("moves focus to the preferences link when the banner goes", async () => {
    await renderWithConsent(newStore(), <CookiePreferencesButton />);
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false, marketing: false }));
    await userEvent.click(screen.getByRole("button", { name: "Reject all" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cookie preferences" })).toHaveFocus());
  });
});

describe("Global Privacy Control", () => {
  it("says the signal was honoured and shows the purpose it denies switched off and unchangeable", async () => {
    stub.state = wireState(
      [wirePurpose("necessary"), wirePurpose("analytics"), wirePurpose("marketing", { gpc_locked: true })],
      { gpc: true },
    );
    await renderWithConsent(newStore({ gpcSignal: () => true }));
    expect(screen.getByTestId("gpc-notice")).toHaveTextContent("Global Privacy Control");
    await userEvent.click(screen.getByRole("button", { name: "Manage preferences" }));
    const marketing = screen.getByRole("switch", { name: "Marketing" });
    expect(marketing).toBeDisabled();
    expect(marketing).not.toBeChecked();
    expect(screen.getByRole("switch", { name: "Analytics" })).toBeEnabled();
  });

  it("says nothing when the server did not apply GPC", async () => {
    await renderWithConsent(newStore());
    expect(screen.queryByTestId("gpc-notice")).not.toBeInTheDocument();
  });
});

describe("an unreachable server", () => {
  it("shows no banner: it can't say what to ask, so nothing is granted and nothing nags", async () => {
    server.use(http.get(BASE, () => HttpResponse.error()));
    await renderWithConsent(newStore());
    expect(banner()).not.toBeInTheDocument();
  });
});
