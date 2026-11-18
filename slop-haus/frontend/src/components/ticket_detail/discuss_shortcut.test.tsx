import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { CONTEXT_HINT_TOKENS } from "@/lib/context_level";
import { makeConductor, makeConversation } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import DiscussShortcut from "./discuss_shortcut";

const SESSION = "55555555-5555-4555-8555-555555555555";

function serve(session: Partial<ReturnType<typeof makeConversation>>) {
  vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c1" })], others: [] });
  vi.spyOn(apiClient, "getConductorHost").mockResolvedValue({ online: true });
  vi.spyOn(apiClient, "listConversations").mockResolvedValue([
    makeConversation({ id: SESSION, conductor_id: "c1", state: "ready", ...session }),
  ]);
}

// Enabled once the sessions have loaded, which is when the click knows
// whether to offer the choices.
async function entryButton() {
  const button = await screen.findByRole("button", { name: "Discuss in Remote Control" });
  await waitFor(() => expect(button).toBeEnabled());
  return button;
}

async function openChoices() {
  await userEvent.click(await entryButton());
}

describe("DiscussShortcut", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("opens Remote Control with the discuss prompt as the draft, and sends nothing", async () => {
    serve({ context_tokens: 0 });
    const start = vi.spyOn(apiClient, "startConversation");
    const send = vi.spyOn(apiClient, "sendConversationMessage");
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await userEvent.click(await entryButton());

    expect(router.state.location.pathname).toBe("/remote-control");
    expect(router.state.location.search).toEqual({ draft: "discuss slop-haus #42" });
    expect(start).not.toHaveBeenCalled();
    expect(send).not.toHaveBeenCalled();
  });

  it("offers no choice when the connected session's context is at the threshold", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS, can_compact: true });
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await waitFor(() => expect(router.state.location.pathname).toBe("/remote-control"));
    expect(screen.queryByRole("button", { name: "Clear and discuss" })).toBeNull();
  });

  it("offers clear, compact and just-discuss above the threshold, once clicked", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1, can_compact: true });
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);
    await openChoices();
    expect(router.state.location.pathname).not.toBe("/remote-control");

    expect(await screen.findByRole("button", { name: "Clear and discuss" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Compact and discuss" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Just discuss" })).toBeTruthy();
  });

  it("hides compact when the session cannot", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1, can_compact: false });
    await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await screen.findByRole("button", { name: "Clear and discuss" });
    expect(screen.queryByRole("button", { name: "Compact and discuss" })).toBeNull();
  });

  it("clears first, then opens the session with the draft", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1 });
    const order: string[] = [];
    vi.spyOn(apiClient, "clearConversation").mockImplementation(async () => {
      order.push("clear");
      return makeConversation();
    });
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await userEvent.click(await screen.findByRole("button", { name: "Clear and discuss" }));

    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/remote-control"));
    expect(order).toEqual(["clear"]);
    expect(apiClient.clearConversation).toHaveBeenCalledWith("c1", SESSION, { interrupt: true });
    expect(router.state.location.search).toEqual({ conductor: "c1", session: SESSION, draft: "discuss slop-haus #42" });
  });

  it("does not open the session when the clear fails", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1 });
    vi.spyOn(apiClient, "clearConversation").mockRejectedValue(new Error("boom"));
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await userEvent.click(await screen.findByRole("button", { name: "Clear and discuss" }));

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(router.state.location.pathname).not.toBe("/remote-control");
  });

  it("compacts first when asked", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1, can_compact: true });
    let release: (c: ReturnType<typeof makeConversation>) => void = () => {};
    const compact = vi.spyOn(apiClient, "compactConversation").mockReturnValue(new Promise((resolve) => (release = resolve)));
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await userEvent.click(await screen.findByRole("button", { name: "Compact and discuss" }));
    await vi.waitFor(() => expect(compact).toHaveBeenCalled());
    expect(router.state.location.pathname).not.toBe("/remote-control");
    release(makeConversation());

    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/remote-control"));
    expect(compact).toHaveBeenCalledWith("c1", SESSION, { interrupt: true });
  });

  it("just discuss opens the session without changing the context", async () => {
    serve({ context_tokens: CONTEXT_HINT_TOKENS + 1 });
    const clear = vi.spyOn(apiClient, "clearConversation");
    const compact = vi.spyOn(apiClient, "compactConversation");
    const { router } = await renderSignedIn(<DiscussShortcut ticketId={42} />);

    await openChoices();
    await userEvent.click(await screen.findByRole("button", { name: "Just discuss" }));

    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/remote-control"));
    expect(clear).not.toHaveBeenCalled();
    expect(compact).not.toHaveBeenCalled();
  });
});
