import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { BASE, ServerStub, decidedState, freshState, newStore, wirePurpose, wireState } from "./test/fixtures";
import { renderWithConsent } from "./test/render";
import DoNotSellButton from "./do_not_sell_button";
import LegalFooterLinks from "../legal_docs/legal_footer_links";

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

const button = () => screen.getByRole("button", { name: "Do Not Sell or Share My Personal Information" });

describe("DoNotSellButton", () => {
  it("records a denial of marketing, as a settings decision, and says it was saved", async () => {
    stub.state = decidedState({ analytics: false, marketing: true });
    await renderWithConsent(newStore(), <DoNotSellButton />);

    await userEvent.click(button());

    await waitFor(() => expect(stub.puts).toHaveLength(1));
    expect(stub.puts[0].source).toBe("settings");
    expect(stub.puts[0].decisions).toEqual([expect.objectContaining({ purpose: "marketing", granted: false })]);
    expect(await screen.findByText("Your choice was saved.")).toBeInTheDocument();
  });

  it("is one footer list item with its outcome, apart from its sibling entries", async () => {
    stub.state = decidedState({ analytics: false, marketing: true });
    await renderWithConsent(
      newStore(),
      <LegalFooterLinks href={(k) => `/legal/${k}`} links={[]}>
        <a href="/request">Privacy request</a>
        <DoNotSellButton />
      </LegalFooterLinks>,
    );

    await userEvent.click(button());

    const outcome = await screen.findByText("Your choice was saved.");
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).not.toContainElement(button());
    expect(within(items[1]).getByRole("button", { name: /Do Not Sell/ })).toBeInTheDocument();
    expect(items[1]).toContainElement(outcome);
  });

  it("denies every purpose it is told are sale or share, and only those", async () => {
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics", { granted: true, needs_decision: false }),
      wirePurpose("marketing", { granted: true, needs_decision: false }),
      wirePurpose("ad_measurement", { granted: true, needs_decision: false }),
    ]);
    await renderWithConsent(newStore(), <DoNotSellButton purposes={["marketing", "ad_measurement"]} />);

    await userEvent.click(button());

    await waitFor(() => expect(stub.puts).toHaveLength(1));
    expect(stub.puts[0].source).toBe("settings");
    expect(stub.puts[0].decisions).toHaveLength(2);
    expect(stub.puts[0].decisions).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ purpose: "marketing", granted: false }),
        expect.objectContaining({ purpose: "ad_measurement", granted: false }),
      ]),
    );
  });

  it("is there, and says so, when no sale or sharing purpose is offered", async () => {
    stub.state = wireState([wirePurpose("necessary"), wirePurpose("analytics")]);
    await renderWithConsent(newStore(), <DoNotSellButton />);

    await userEvent.click(button());

    expect(await screen.findByText(/nothing to turn off/)).toBeInTheDocument();
    expect(stub.puts).toHaveLength(0);
  });

  it("waits for the server's answer rather than saying there is nothing to turn off", async () => {
    let answer: () => void = () => undefined;
    const held = new Promise<void>((r) => (answer = r));
    server.use(
      http.get(BASE, async () => {
        await held;
        return HttpResponse.json(decidedState({ analytics: false, marketing: true }));
      }),
    );
    const store = newStore();
    const rendered = renderWithConsent(store, <DoNotSellButton />);
    await Promise.resolve();
    expect(button()).toBeDisabled();
    answer();
    await rendered;
    await waitFor(() => expect(button()).toBeEnabled());
  });

  it("says when the choice could not be saved", async () => {
    stub.state = decidedState({ analytics: false, marketing: true });
    stub.nextPut = () => HttpResponse.json({ code: "unknown" }, { status: 500 });
    await renderWithConsent(newStore(), <DoNotSellButton />);

    await userEvent.click(button());

    expect(await screen.findByText("Your choice could not be saved. Try again.")).toBeInTheDocument();
  });
});
