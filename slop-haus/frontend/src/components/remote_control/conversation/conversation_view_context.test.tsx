import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, type ConversationEvent } from "@/api";
import { makeConductor, makeConversation, makeConversationDetail, makeConversationEvent } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { ProposalsContext } from "@/components/proposals";
import ConversationView from "./conversation_view";
import { recentEventsOf } from "@/test/recent_events";

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const CONVERSATION = "44444444-4444-4444-8444-444444444444";

function ev(seq: number, kind: ConversationEvent["kind"], body = "", turn: number | null = null) {
  return makeConversationEvent({ seq, kind, body, turn });
}

async function show(history: ConversationEvent[], detail = makeConversationDetail()) {
  vi.spyOn(apiClient, "getConversation").mockResolvedValue(detail);
  vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(history));
  await renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
      <UserSettingsProvider>
        <ConversationView conductor={conductor} conversationId={CONVERSATION} />
      </UserSettingsProvider>
    </ProposalsContext.Provider>,
  );
}

let push: (event: ConversationEvent) => void;

const idle = [ev(1, "user", "hi"), ev(2, "assistant", "hello", 1), ev(3, "turn_end", "", 1)];
const replying = [ev(1, "user", "hi"), ev(2, "assistant", "thinking", 1)];

describe("ConversationView context control", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([makeConversation({ id: CONVERSATION, state: "ready" })]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "streamConversation").mockImplementation((_c, _id, _after, onEvent) => {
      push = onEvent;
      return () => {};
    });
  });

  it("shows the context meter, and no hint below 150k", async () => {
    await show(idle, makeConversationDetail({ context_tokens: 120_000 }));
    expect(await screen.findByText("Context: 120k")).toBeInTheDocument();
    expect(screen.queryByText(/consider Clear or Compact/)).not.toBeInTheDocument();
  });

  it("hints above 150k and hides the meter when unknown", async () => {
    await show(idle, makeConversationDetail({ context_tokens: 214_000 }));
    expect(await screen.findByRole("status", { name: "Context size" })).toHaveTextContent("Context: 214k");
    expect(screen.getByText(/consider Clear or Compact/)).toBeInTheDocument();
  });

  it("has no meter for unknown context", async () => {
    await show(idle);
    await screen.findByRole("button", { name: "Clear" });
    expect(screen.queryByText(/^Context:/)).not.toBeInTheDocument();
  });

  it("offers Compact only when the session can compact", async () => {
    await show(idle);
    await screen.findByRole("button", { name: "Clear" });
    expect(screen.queryByRole("button", { name: "Compact" })).not.toBeInTheDocument();
  });

  it("clears after a confirmation, without interrupt when idle", async () => {
    const clear = vi.spyOn(apiClient, "clearConversation").mockResolvedValue(makeConversation());
    await show(idle);
    await userEvent.click(await screen.findByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("alertdialog", { name: "Confirm clear" });
    expect(within(dialog).queryByText(/interrupt/)).not.toBeInTheDocument();
    expect(clear).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(clear).toHaveBeenCalledWith("c-1", CONVERSATION, { interrupt: false }));
  });

  it("says compacting a running reply interrupts it, and sends interrupt", async () => {
    const compact = vi.spyOn(apiClient, "compactConversation").mockResolvedValue(makeConversation());
    await show(replying, makeConversationDetail({ can_compact: true }));
    await userEvent.click(await screen.findByRole("button", { name: "Compact" }));
    const dialog = screen.getByRole("alertdialog", { name: "Confirm compact" });
    expect(within(dialog).getByText(/will interrupt the reply in progress/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Compact" }));
    await waitFor(() => expect(compact).toHaveBeenCalledWith("c-1", CONVERSATION, { interrupt: true }));
  });

  it("calls nothing when the confirmation is declined", async () => {
    const clear = vi.spyOn(apiClient, "clearConversation").mockResolvedValue(makeConversation());
    await show(idle);
    await userEvent.click(await screen.findByRole("button", { name: "Clear" }));
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(clear).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("draws dividers for cleared and compacted context", async () => {
    await show([...idle, ev(4, "context_cleared"), ev(5, "context_compacted")]);
    const transcript = await screen.findByRole("list", { name: "Transcript" });
    expect(within(transcript).getByText("Context cleared")).toBeInTheDocument();
    expect(within(transcript).getByText("Context compacted")).toBeInTheDocument();
  });

  it("clears a running reply with interrupt", async () => {
    const clear = vi.spyOn(apiClient, "clearConversation").mockResolvedValue(makeConversation());
    await show(replying);
    await userEvent.click(await screen.findByRole("button", { name: "Clear" }));
    const dialog = screen.getByRole("alertdialog", { name: "Confirm clear" });
    expect(within(dialog).getByText(/will interrupt the reply in progress/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(clear).toHaveBeenCalledWith("c-1", CONVERSATION, { interrupt: true }));
  });

  it("offers neither action for a session with no process", async () => {
    await show(idle, makeConversationDetail({ state: "stopped", can_compact: true }));
    await screen.findByRole("button", { name: "Delete" });
    expect(screen.queryByRole("button", { name: "Clear" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Compact" })).not.toBeInTheDocument();
  });

  it("marks dividers with their kind's class", async () => {
    await show([...idle, ev(4, "context_cleared")]);
    const divider = (await screen.findByText("Context cleared")).closest("li");
    expect(divider).toHaveClass("conversation__note--context_cleared");
  });

  it("reads the detail again when the stream brings a context event, so the meter follows", async () => {
    await show(idle, makeConversationDetail({ context_tokens: 214_000 }));
    expect(await screen.findByText("Context: 214k")).toBeInTheDocument();
    vi.mocked(apiClient.getConversation).mockResolvedValue(makeConversationDetail({ context_tokens: 0 }));
    act(() => push(ev(4, "context_cleared")));
    await waitFor(() => expect(screen.queryByText("Context: 214k")).not.toBeInTheDocument());
  });
});
