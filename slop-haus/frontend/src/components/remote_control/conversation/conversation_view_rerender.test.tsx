import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import { apiClient, type ConversationEvent } from "@/api";
import { makeConductor, makeConversation, makeConversationDetail, makeConversationEvent } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { ProposalsContext } from "@/components/proposals";
import { recentEventsOf } from "@/test/recent_events";
import ConversationView from "./conversation_view";

// Counts the parses, so a test can tell which messages were parsed again.
const parsed = vi.hoisted(() => ({ texts: [] as string[] }));
vi.mock("react-markdown", async () => {
  const actual = await vi.importActual<typeof import("react-markdown")>("react-markdown");
  return {
    ...actual,
    default: (props: { children: string }) => {
      parsed.texts.push(props.children);
      return actual.default(props);
    },
  };
});

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const CONVERSATION = "44444444-4444-4444-8444-444444444444";
const COUNT = 300;

let push: (event: ConversationEvent) => void;

describe("ConversationView re-rendering", () => {
  beforeEach(() => {
    parsed.texts = [];
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([makeConversation({ id: CONVERSATION, state: "ready" })]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "streamConversation").mockImplementation((_c, _id, _after, onEvent) => {
      push = onEvent;
      return vi.fn();
    });
  });

  it("parses only the new message when one streams in to a long transcript", async () => {
    const history = Array.from({ length: COUNT }, (_, i) =>
      makeConversationEvent({ seq: i + 1, kind: i % 2 ? "assistant" : "user", body: `message **${i + 1}**`, turn: null }),
    );
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(history));
    await renderSignedIn(
      <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
        <UserSettingsProvider>
          <ConversationView conductor={conductor} conversationId={CONVERSATION} />
        </UserSettingsProvider>
      </ProposalsContext.Provider>,
    );
    await screen.findByText(String(COUNT));
    const initial = parsed.texts.length;
    expect(initial).toBeGreaterThan(10);

    parsed.texts = [];
    act(() => push(makeConversationEvent({ seq: COUNT + 1, kind: "user", body: "the **new** one", turn: null })));
    await screen.findByText("new");

    expect(parsed.texts).toEqual(["the **new** one"]);
  });
});
