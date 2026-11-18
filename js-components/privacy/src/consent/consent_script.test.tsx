import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { setupServer } from "msw/node";
import ConsentScript from "./consent_script";
import { ConsentProvider } from "./consent_provider";
import { useConsent, useConsentCheck } from "./consent_context";
import { BASE, ServerStub, decidedState, freshState, newStore } from "./test/fixtures";
import { renderWithConsent } from "./test/render";

const stub = new ServerStub();
const server = setupServer(...stub.handlers);

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  stub.state = freshState();
  stub.nextPut = null;
  stub.puts = [];
});
afterAll(() => server.close());

const grant = (analytics: boolean) => () => HttpResponse.json(decidedState({ analytics, marketing: false }));

describe("ConsentScript", () => {
  it("loads nothing until the purpose is granted, then loads once", async () => {
    const store = newStore();
    const load = vi.fn();
    await renderWithConsent(store, <ConsentScript purpose="analytics" id="vendor" load={load} />, { ui: false });
    expect(load).not.toHaveBeenCalled();
    stub.nextPut = grant(true);
    await act(() => store.decide("banner", [{ purpose: "analytics", granted: true }]));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
  });

  it("loads nothing when the visitor rejects", async () => {
    const store = newStore();
    const load = vi.fn();
    await renderWithConsent(store, <ConsentScript purpose="analytics" id="vendor" load={load} />, { ui: false });
    stub.nextPut = grant(false);
    await act(() => store.decideAll("banner", false));
    expect(load).not.toHaveBeenCalled();
  });

  it("does not load when the decision arrives after the component has unmounted", async () => {
    const store = newStore();
    const load = vi.fn();
    const view = await renderWithConsent(store, <ConsentScript purpose="analytics" id="vendor" load={load} />, {
      ui: false,
    });
    let answer: () => void = () => undefined;
    const held = new Promise<void>((r) => (answer = r));
    server.use(
      http.put(BASE, async () => {
        await held;
        return HttpResponse.json(decidedState({ analytics: true, marketing: false }));
      }),
    );
    const deciding = store.decide("banner", [{ purpose: "analytics", granted: true }]);
    view.unmount();
    answer();
    await deciding;
    expect(store.isGranted("analytics")).toBe(true);
    expect(load).not.toHaveBeenCalled();
  });

  it("loads once under StrictMode's double mount, and tears down once", async () => {
    stub.state = decidedState({ analytics: true });
    const store = newStore();
    const teardown = vi.fn();
    const load = vi.fn(async () => teardown);
    const view = render(
      <StrictMode>
        <ConsentProvider store={store} ui={false}>
          <ConsentScript purpose="analytics" id="vendor" load={load} />
        </ConsentProvider>
      </StrictMode>,
    );
    await store.load();
    await waitFor(() => expect(load).toHaveBeenCalled());
    // The first mount's load was undone before the second began.
    expect(load.mock.calls.length - teardown.mock.calls.length).toBe(1);
    view.unmount();
    expect(teardown).toHaveBeenCalledTimes(load.mock.calls.length);
  });

  it("loads once for two components sharing an id", async () => {
    stub.state = decidedState({ analytics: true });
    const load = vi.fn();
    await renderWithConsent(
      newStore(),
      <>
        <ConsentScript purpose="analytics" id="vendor" load={load} />
        <ConsentScript purpose="analytics" id="vendor" load={load} />
      </>,
      { ui: false },
    );
    await waitFor(() => expect(load).toHaveBeenCalled());
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("tears the script down when consent is withdrawn", async () => {
    stub.state = decidedState({ analytics: true });
    const store = newStore();
    const teardown = vi.fn();
    const load = vi.fn(async () => teardown);
    await renderWithConsent(store, <ConsentScript purpose="analytics" id="vendor" load={load} />, { ui: false });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    await Promise.resolve();
    stub.nextPut = grant(false);
    await act(() => store.decide("settings", [{ purpose: "analytics", granted: false }]));
    expect(teardown).toHaveBeenCalledTimes(1);
  });

  it("undoes a script that finished loading after consent was withdrawn", async () => {
    stub.state = decidedState({ analytics: true });
    const store = newStore();
    const teardown = vi.fn();
    let finish: () => void = () => undefined;
    const load = vi.fn(
      () =>
        new Promise<() => void>((resolve) => {
          finish = () => resolve(teardown);
        }),
    );
    await renderWithConsent(store, <ConsentScript purpose="analytics" id="vendor" load={load} />, { ui: false });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    stub.nextPut = grant(false);
    await act(() => store.decide("settings", [{ purpose: "analytics", granted: false }]));
    expect(teardown).not.toHaveBeenCalled();
    finish();
    await waitFor(() => expect(teardown).toHaveBeenCalledTimes(1));
  });
});

describe("useConsent", () => {
  function Probe({ purpose }: { purpose: string }) {
    const c = useConsent(purpose);
    return (
      <p data-testid="probe">
        {c.available ? "available" : "absent"}/{c.granted ? "granted" : "denied"}/{c.loading ? "loading" : "ready"}
      </p>
    );
  }

  it("reads the state from the server, and denies while loading", async () => {
    stub.state = decidedState({ analytics: true });
    render(
      <ConsentProvider store={newStore()} ui={false}>
        <Probe purpose="analytics" />
      </ConsentProvider>,
    );
    expect(screen.getByTestId("probe")).toHaveTextContent("absent/denied/loading");
    await waitFor(() => expect(screen.getByTestId("probe")).toHaveTextContent("available/granted/ready"));
  });

  it("reports a purpose the server doesn't offer as absent and denied", async () => {
    await renderWithConsent(newStore(), <Probe purpose="interaction_recording" />, { ui: false });
    expect(screen.getByTestId("probe")).toHaveTextContent("absent/denied/ready");
  });

  it("throws outside a provider", () => {
    const quiet = vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(() => render(<Probe purpose="analytics" />)).toThrow(/ConsentProvider/);
    quiet.mockRestore();
  });
});

describe("the consent check for @slop-factory/logging", () => {
  it("is the live analytics grant, held as one stable function", async () => {
    stub.state = decidedState({ analytics: true });
    const store = newStore();
    const seen: (() => boolean)[] = [];
    function Capture() {
      seen.push(useConsentCheck("analytics"));
      return null;
    }
    await renderWithConsent(store, <Capture />, { ui: false });
    const check = seen[seen.length - 1];
    expect(check()).toBe(true);
    stub.nextPut = grant(false);
    await act(() => store.decide("settings", [{ purpose: "analytics", granted: false }]));
    expect(check()).toBe(false);
    expect(new Set(seen).size).toBe(1);
  });
});
