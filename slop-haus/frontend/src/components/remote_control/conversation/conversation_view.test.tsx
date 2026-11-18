import { expectOneRow } from "../composer_row";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type ConversationEvent } from "@/api";
import {
  makeConductor,
  makeConversation,
  makeConversationDetail,
  makeConversationEvent,
  makeProposal,
} from "@/test/fixtures";
import { makeAttachment } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { ProposalsContext } from "@/components/proposals";
import ConversationView from "./conversation_view";
import { NO_EVENTS, recentEventsOf } from "@/test/recent_events";

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const CONVERSATION = "44444444-4444-4444-8444-444444444444";

// The stream's callback, so a test can say what arrives on it.
let push: (event: ConversationEvent) => void;
let closeStream: ReturnType<typeof vi.fn>;

function ev(seq: number, kind: ConversationEvent["kind"], body: string, turn: number | null = null, block: string | null = null) {
  return makeConversationEvent({ seq, kind, body, turn, block });
}

function serve(history: ConversationEvent[], detail = makeConversationDetail()) {
  vi.spyOn(apiClient, "getConversation").mockResolvedValue(detail);
  vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(history));
}

async function show(proposals: ReturnType<typeof makeProposal>[] = [], onChanged?: () => void, onBack?: () => void, onDeleted?: () => void) {
  await renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals, reload: async () => {} }}>
      <UserSettingsProvider>
        <ConversationView conductor={conductor} conversationId={CONVERSATION} onChanged={onChanged} onBack={onBack} onDeleted={onDeleted} />
      </UserSettingsProvider>
    </ProposalsContext.Provider>,
  );
}

describe("ConversationView", () => {
  beforeEach(() => {
    closeStream = vi.fn();
    // The open session is the machine's live one, so a send switches nothing.
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([makeConversation({ id: CONVERSATION, state: "ready" })]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "streamConversation").mockImplementation((_c, _id, _after, onEvent) => {
      push = onEvent;
      return closeStream as unknown as () => void;
    });
  });

  it("shows the history, which machine it runs on and that its host is online", async () => {
    serve([ev(1, "user", "What does this do?"), ev(2, "assistant", "It files tickets."), ev(3, "turn_end", "", 1)]);
    await show();

    const transcript = await screen.findByRole("list", { name: "Transcript" });
    expect(within(transcript).getByText("What does this do?")).toBeInTheDocument();
    expect(within(transcript).getByText("It files tickets.")).toBeInTheDocument();
    expect(screen.getByText("my-laptop", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("Online")).toBeInTheDocument();
  });

  it("holds only list items in the transcript list, its scroll anchor included", async () => {
    serve([ev(1, "user", "hi"), ev(2, "assistant", "hello", 1)]);
    await show();

    const transcript = await screen.findByRole("list", { name: "Transcript" });
    await within(transcript).findByText("hello");
    expect(Array.from(transcript.children).map((child) => child.tagName)).toSatisfy((tags: string[]) =>
      tags.every((tag) => tag === "LI"),
    );
  });

  it("shows a repo state in its transcript, ahead and behind its upstream included", async () => {
    const repo = JSON.stringify({
      branch: "ticket-9-x", head: "abcdef0123", dirty: false, dirty_count: 0, upstream: "origin/ticket-9-x", ahead: 2, behind: 1,
      commits: [{ sha: "a".repeat(40), subject: "add c" }], pushes: [], changed_paths: [], changed_total: 0,
    });
    serve([ev(1, "user", "go"), ev(2, "assistant", "done", 1), ev(3, "turn_end", "", 1), ev(4, "repo_state", repo, 1)]);
    await show();

    expect(await screen.findByText("On ticket-9-x at abcdef0, 1 commit this turn (add c), 2 ahead, 1 behind")).toBeInTheDocument();
  });

  it("does not talk of modes, checkouts or updates", async () => {
    serve([ev(1, "user", "hi")]);
    await show();
    await screen.findByText("hi");

    expect(screen.queryByText(/Full|Read-only|Mode|Checkout|Update|Resume/)).not.toBeInTheDocument();
  });

  it("opens the stream after the history it has, and closes it on leaving", async () => {
    serve([ev(1, "user", "hi"), ev(2, "assistant", "hello", 1)]);
    await show();
    await screen.findByText("hello");

    expect(apiClient.streamConversation).toHaveBeenCalledWith("c-1", CONVERSATION, 2, expect.any(Function));
  });

  it("closes the stream when the view goes", async () => {
    serve([]);
    const { unmount } = await renderSignedIn(
      <UserSettingsProvider>
        <ConversationView conductor={conductor} conversationId={CONVERSATION} />
      </UserSettingsProvider>,
    );
    await screen.findByText("Nothing said yet.");

    unmount();

    expect(closeStream).toHaveBeenCalled();
  });

  it("grows a reply as the stream brings it, and says it is replying until the turn ends", async () => {
    serve([ev(1, "user", "hi")]);
    await show();
    const note = await screen.findByText(/^Working…/);
    expect(note.closest("li")?.querySelector(".conversation__spinner")).toHaveAttribute("aria-hidden", "true");

    act(() => push(ev(2, "assistant", "Hel")));
    act(() => push(ev(3, "assistant", "lo there")));
    expect(screen.getByText("Hello there")).toBeInTheDocument();
    expect(screen.getByText(/^Working…/)).toBeInTheDocument();

    act(() => push(ev(4, "turn_end", "", 1)));
    expect(screen.queryByText(/^Working…/)).not.toBeInTheDocument();
    expect(document.querySelector(".conversation__spinner")).toBeNull();
  });

  it("shows each model message as its own paragraph, with the tool calls between them", async () => {
    const call = (status: string) => JSON.stringify({ call_id: "t1", tool: "Bash", line: "Check pilot-3", status });
    serve([
      ev(1, "user", "go"),
      ev(2, "assistant", "Checking pilot-3:", null, "m1"),
      ev(3, "activity", call("running")),
      ev(4, "activity", call("done")),
      ev(5, "assistant", "All fine.", null, "m2"),
    ]);
    await show();

    const first = await screen.findByText("Checking pilot-3:");
    const second = screen.getByText("All fine.");
    expect(first).not.toBe(second);
    expect(screen.getByText("Check pilot-3")).toBeInTheDocument();
    const details = document.querySelector("details.conversation__activity") as HTMLDetailsElement;
    expect(details.open).toBe(true);
    expect(screen.getByText(/^Working…/)).toHaveTextContent("Bash: Check pilot-3");

    act(() => push(ev(6, "turn_end", "", 1)));
    expect((document.querySelector("details.conversation__activity") as HTMLDetailsElement).open).toBe(false);
  });

  it("shows replies from before block ids as one message, as it did", async () => {
    serve([ev(1, "user", "go"), ev(2, "assistant", "Hel"), ev(3, "assistant", "lo")]);
    await show();
    expect(await screen.findByText("Hello")).toBeInTheDocument();
  });

  it("offers the prompts the owner sent in this conversation, and not the assistant's words, on Up", async () => {
    serve([
      ev(1, "user", "first"),
      ev(2, "assistant", "an answer", 1),
      ev(3, "turn_end", "", 1),
      ev(4, "user", "second\nmore"),
    ]);
    await show();
    await screen.findByRole("list", { name: "Transcript" });

    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "{ArrowUp}");

    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["first", "second…"]);
  });

  it("adds a prompt that arrives later to the history", async () => {
    serve([ev(1, "user", "first")]);
    await show();
    await screen.findByRole("list", { name: "Transcript" });

    act(() => push(ev(2, "user", "later")));
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "{ArrowUp}");

    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["first", "later"]);
  });

  it("shows an event once when the stream sends it again", async () => {
    serve([ev(1, "user", "hi"), ev(2, "assistant", "same", 1)]);
    await show();
    await screen.findByText("same");

    act(() => push(ev(2, "assistant", "same", 1)));

    expect(screen.getAllByText("same")).toHaveLength(1);
  });

  describe("markdown", () => {
    const messages = () => [
      ev(1, "user", "**mine** <b>raw</b>"),
      ev(2, "assistant", '<img src="https://evil.example/x.png" onerror="alert(1)"> [link](https://evil.example) and `code`', 1),
    ];
    const settingOff = () =>
      vi.spyOn(apiClient, "listMySettings").mockResolvedValue([{ name: "render_markdown", type: "bool", bool_val: false, is_default: false } as never]);

    it("renders the person's and the model's messages as markdown, never as markup", async () => {
      serve(messages());
      await show();

      const transcript = await screen.findByRole("list", { name: "Transcript" });
      await waitFor(() => expect(transcript.querySelector("strong")?.textContent).toBe("mine"));
      expect(transcript.querySelector("img, b")).toBeNull();
      expect(within(transcript).getByRole("link", { name: "link" })).toHaveAttribute("href", "https://evil.example");
      expect(transcript.querySelector("code")?.textContent).toBe("code");
    });

    it("shows the exact source, escaped, with a copy button per message, when the setting is off", async () => {
      settingOff();
      serve(messages());
      await show();

      const transcript = await screen.findByRole("list", { name: "Transcript" });
      await waitFor(() => expect(within(transcript).getByText("**mine** <b>raw</b>")).toBeInTheDocument());
      expect(transcript.querySelector("img, a, b, strong")).toBeNull();
      expect(within(transcript).getByText(/\[link\]\(https:\/\/evil\.example\)/)).toBeInTheDocument();
      expect(within(transcript).getAllByRole("button", { name: "Copy" })).toHaveLength(2);
    });

    it("flips the page with the toggle without saving, and does not touch tool output notes", async () => {
      const save = vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue({} as never);
      serve(messages());
      await show();

      const transcript = await screen.findByRole("list", { name: "Transcript" });
      await waitFor(() => expect(transcript.querySelector("strong")).not.toBeNull());
      await userEvent.click(screen.getByRole("button", { name: "Plain" }));
      expect(transcript.querySelector("strong")).toBeNull();
      expect(within(transcript).getByText("**mine** <b>raw</b>")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Rendered" }));
      await waitFor(() => expect(transcript.querySelector("strong")).not.toBeNull());
      expect(save).not.toHaveBeenCalled();
    });
  });

  it("says when the host is offline", async () => {
    serve([], makeConversationDetail({ host_online: false, state: "stopped" }));
    await show();

    expect(await screen.findByText("Offline")).toBeInTheDocument();
    expect(screen.getByText(/host is not running/)).toBeInTheDocument();
  });

  it("labels a live session on an offline host Offline, as the sidebar does", async () => {
    serve([], makeConversationDetail({ host_online: false, state: "ready" }));
    await show();

    expect(await screen.findByText("State")).toBeInTheDocument();
    expect(screen.getAllByText("Offline")).toHaveLength(2);
    expect(screen.queryByText("Connected")).not.toBeInTheDocument();
  });

  it("sends a message with an id of its own, and shows it without waiting for the stream", async () => {
    serve([]);
    const send = vi.spyOn(apiClient, "sendConversationMessage").mockResolvedValue(ev(1, "user", "ping"));
    await show();

    await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "ping");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(send).toHaveBeenCalledWith("c-1", CONVERSATION, { text: "ping", clientId: expect.stringMatching(/^[0-9a-f-]{36}$/) });
    expect(await screen.findByText("ping")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("");
  });

  it("sends an uploaded file's id with the message, with or without text, and clears the chip", async () => {
    serve([]);
    vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "att-1" }));
    const send = vi.spyOn(apiClient, "sendConversationMessage").mockResolvedValue(ev(1, "user", ""));
    await show();

    await userEvent.upload(await screen.findByLabelText("Choose images or videos to attach"), new File([new Uint8Array(4)], "shot.png", { type: "image/png" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send).toHaveBeenCalledWith("c-1", CONVERSATION, { text: "", clientId: expect.any(String), attachmentIds: ["att-1"] });
    await waitFor(() => expect(screen.queryByText("shot.png")).toBeNull());
  });

  it("keeps the chips when the send fails", async () => {
    serve([]);
    vi.spyOn(apiClient, "uploadConversationAttachment").mockResolvedValue(makeAttachment({ id: "att-1" }));
    vi.spyOn(apiClient, "sendConversationMessage").mockRejectedValue(new ApiError(503, "try again"));
    await show();

    await userEvent.upload(await screen.findByLabelText("Choose images or videos to attach"), new File([new Uint8Array(4)], "shot.png", { type: "image/png" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText("try again")).toBeInTheDocument();
    expect(screen.getByText("shot.png")).toBeInTheDocument();
  });

  it("sends the same id again for the same text after a failure, so a lost reply stores it once", async () => {
    serve([]);
    const send = vi
      .spyOn(apiClient, "sendConversationMessage")
      .mockRejectedValueOnce(new ApiError(503, "try again"))
      .mockResolvedValueOnce(ev(1, "user", "ping"));
    await show();
    await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "ping");

    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("try again");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls[1][2].clientId).toBe(send.mock.calls[0][2].clientId);
  });

  it("shows the remote control's pending proposals inline with a link to approve them, and no one else's", async () => {
    serve([]);
    await show([
      makeProposal({ id: "p1", agent_name: "my-laptop", ticket_id: 7, action_line: "Open #7 to the queue.", text: "<script>x</script>" }),
      makeProposal({ id: "p2", agent_name: "another", ticket_id: 7, action_line: "Not mine." }),
    ]);

    const list = await screen.findByRole("list", { name: "Proposed by this remote control" });
    expect(within(list).getByText(/Open #7 to the queue/)).toBeInTheDocument();
    expect(within(list).getByText("<script>x</script>")).toBeInTheDocument();
    expect(within(list).getByRole("link", { name: /approve on the Approvals tab/ })).toHaveAttribute("href", "/approvals");
    expect(screen.queryByText(/Not mine/)).not.toBeInTheDocument();
  });

  it("collapses a very long proposal to a preview that can be expanded, and still shows the log", async () => {
    serve([ev(1, "user", "hello there"), ev(2, "assistant", "hi", 1), ev(3, "turn_end", "", 1)]);
    const text = Array.from({ length: 200 }, (_, i) => `line ${i}`).join("\n");
    await show([makeProposal({ id: "p1", agent_name: "my-laptop", action_line: "Do a thing.", text })]);

    const list = await screen.findByRole("list", { name: "Proposed by this remote control" });
    expect(list).toHaveClass("conversation__proposals");
    expect(within(list).queryByText(/line 150/)).not.toBeInTheDocument();
    expect(within(list).getByText("line 0 line 1…")).toBeInTheDocument();
    expect(await screen.findByText("hello there")).toBeInTheDocument();

    const toggle = within(list).getByRole("button", { name: "Show details of: Do a thing." });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const full = within(list).getByText(/line 150/);
    expect(full).toHaveClass("approvals__text-body");
    await userEvent.click(within(list).getByRole("button", { name: "Hide details of: Do a thing." }));
    expect(within(list).queryByText(/line 150/)).not.toBeInTheDocument();
  });

  it("collapses each of several proposals on its own", async () => {
    serve([]);
    const long = (tag: string) => Array.from({ length: 10 }, (_, i) => `${tag} ${i}`).join("\n");
    await show([
      makeProposal({ id: "p1", agent_name: "my-laptop", action_line: "First.", text: long("aaa") }),
      makeProposal({ id: "p2", agent_name: "my-laptop", action_line: "Second.", text: long("bbb") }),
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Show details of: First." }));
    expect(screen.getByText(/aaa 9/)).toBeInTheDocument();
    expect(screen.queryByText(/bbb 9/)).not.toBeInTheDocument();
  });

  it("truncates a single very long line in the preview", async () => {
    serve([]);
    await show([makeProposal({ id: "p1", agent_name: "my-laptop", action_line: "Wide.", text: "x".repeat(1000) })]);

    const toggle = await screen.findByRole("button", { name: "Show details of: Wide." });
    expect(screen.getByText(`${"x".repeat(160)}…`)).toBeInTheDocument();
    await userEvent.click(toggle);
    expect(screen.getByText("x".repeat(1000))).toBeInTheDocument();
  });

  describe("disconnecting", () => {
    const replying = [ev(1, "user", "hi"), ev(2, "assistant", "thinking", 1)];
    const idle = [ev(1, "user", "hi"), ev(2, "assistant", "hello", 1), ev(3, "turn_end", "", 1)];

    it("disconnects an idle session at once, with no question, and tells the list", async () => {
      serve(idle);
      const disconnect = vi.spyOn(apiClient, "disconnectConversation").mockResolvedValue(makeConversationDetail());
      const onChanged = vi.fn();
      await show([], onChanged);

      await userEvent.click(await screen.findByRole("button", { name: "Disconnect" }));

      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
      await waitFor(() => expect(disconnect).toHaveBeenCalledWith("c-1", CONVERSATION));
      await waitFor(() => expect(onChanged).toHaveBeenCalled());
    });

    it("asks before stopping a reply in progress, and disconnects only on yes", async () => {
      serve(replying);
      const disconnect = vi.spyOn(apiClient, "disconnectConversation").mockResolvedValue(makeConversationDetail());
      await show();

      await userEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
      const dialog = screen.getByRole("alertdialog");
      expect(within(dialog).getByText("Disconnect and stop the current reply? To stop just this reply, use Stop.")).toBeInTheDocument();
      expect(disconnect).not.toHaveBeenCalled();

      await userEvent.click(within(dialog).getByRole("button", { name: "Keep it" }));
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
      expect(disconnect).not.toHaveBeenCalled();

      await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
      await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Disconnect" }));
      await waitFor(() => expect(disconnect).toHaveBeenCalledOnce());
    });

    it("sticks to the top right, out of the way of Send", async () => {
      serve(idle);
      await show();

      const button = await screen.findByRole("button", { name: "Disconnect" });
      expect(button.closest(".conversation__disconnect")).not.toBeNull();
      expect(screen.getByRole("button", { name: "Send" }).closest(".conversation__disconnect")).toBeNull();
    });

    describe("Delete", () => {
      it("names the session and says the transcript is gone, and deletes only on yes", async () => {
        serve(idle, makeConversationDetail({ title: "Importer work" }));
        const del = vi.spyOn(apiClient, "deleteConversation").mockResolvedValue();
        const onDeleted = vi.fn();
        const onChanged = vi.fn();
        await show([], onChanged, undefined, onDeleted);

        await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
        const dialog = screen.getByRole("alertdialog", { name: "Confirm delete" });
        expect(within(dialog).getByText("Importer work")).toBeInTheDocument();
        expect(dialog).toHaveTextContent("gone for good");
        expect(del).not.toHaveBeenCalled();

        await userEvent.click(within(dialog).getByRole("button", { name: "Keep it" }));
        expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
        expect(del).not.toHaveBeenCalled();

        await userEvent.click(screen.getByRole("button", { name: "Delete" }));
        await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Delete" }));
        await waitFor(() => expect(del).toHaveBeenCalledExactlyOnceWith("c-1", CONVERSATION));
        await waitFor(() => expect(onDeleted).toHaveBeenCalledOnce());
        expect(onChanged).toHaveBeenCalledOnce();
      });

      it("shows the server's reason for a refusal and stays on the session", async () => {
        serve(idle);
        vi.spyOn(apiClient, "deleteConversation").mockRejectedValue(new ApiError(409, "The host has not caught up yet; try again shortly."));
        const onDeleted = vi.fn();
        await show([], undefined, undefined, onDeleted);

        await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
        await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Delete" }));

        expect(await screen.findByRole("alert")).toHaveTextContent("The host has not caught up yet; try again shortly.");
        expect(onDeleted).not.toHaveBeenCalled();
        expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
      });

      it("is offered for a session with no process too", async () => {
        serve(idle, makeConversationDetail({ state: "stopped" }));
        await show();

        expect(await screen.findByRole("button", { name: "Delete" })).toBeInTheDocument();
      });
    });

    it("offers no Disconnect for a session with no process", async () => {
      serve(idle, makeConversationDetail({ state: "stopped" }));
      await show();

      await screen.findByText("State");
      expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
    });
  });

  it("keeps the message box for a session with no process, and says the next message reconnects it", async () => {
    serve([ev(1, "user", "hi")], makeConversationDetail({ state: "stopped" }));
    const send = vi.spyOn(apiClient, "sendConversationMessage").mockResolvedValue(ev(2, "user", "again"));
    // No other session to stop, and the explanation already dismissed.
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([
      { name: REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type: "bool", bool_val: true, is_default: false } as never,
    ]);
    const onChanged = vi.fn();
    await show([], onChanged);

    expect(await screen.findByText(/This session has no connected harness process\. Your next message reconnects it.*previous working directory state/)).toBeInTheDocument();
    expect(screen.queryByText(/Claude/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Resume/ })).not.toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "again");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(send).toHaveBeenCalledWith("c-1", CONVERSATION, { text: "again", clientId: expect.any(String) });
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it("puts the message box and Send in one row, in that order", async () => {
    serve([]);
    await show();

    const send = await screen.findByRole("button", { name: "Send" });
    expectOneRow(screen.getByRole("textbox", { name: "Message" }), send);
  });

  it("reports a conversation that cannot be read", async () => {
    vi.spyOn(apiClient, "getConversation").mockRejectedValue(new ApiError(404, "no such conversation"));
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    await show();

    expect(await screen.findByRole("alert")).toHaveTextContent("no such conversation");
  });

  describe("as a screen of its own (a phone)", () => {
    const idle = [ev(1, "user", "hi"), ev(2, "assistant", "hello", 1), ev(3, "turn_end", "", 1)];

    it("has a bar with the way back, and keeps the facts and Disconnect in its menu", async () => {
      serve(idle);
      const onBack = vi.fn();
      await show([], undefined, onBack);

      await screen.findByRole("list", { name: "Transcript" });
      expect(screen.getByRole("button", { name: "Rename session" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
      expect(screen.queryByText("Host")).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));
      expect(onBack).toHaveBeenCalledTimes(1);

      await userEvent.click(screen.getByRole("button", { name: "Session menu" }));
      expect(screen.getByText("Host")).toBeInTheDocument();
      expect(screen.getByText("Online")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Disconnect" })).toBeInTheDocument();
    });

    it("disconnects from the menu, asking first while a reply is under way", async () => {
      serve([ev(1, "user", "hi"), ev(2, "assistant", "thinking", 1)]);
      const disconnect = vi.spyOn(apiClient, "disconnectConversation").mockResolvedValue(makeConversationDetail());
      await show([], undefined, vi.fn());

      await userEvent.click(await screen.findByRole("button", { name: "Session menu" }));
      await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
      expect(disconnect).not.toHaveBeenCalled();
      await userEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Disconnect" }));
      await waitFor(() => expect(disconnect).toHaveBeenCalledWith("c-1", CONVERSATION));
    });

    it("keeps its notices on the screen, outside the menu", async () => {
      serve(idle, makeConversationDetail({ state: "stopped" }));
      await show([], undefined, vi.fn());

      expect(await screen.findByText(/no connected harness process/)).toBeInTheDocument();
    });

    it("has the way back when the session cannot be read", async () => {
      serve(idle);
      vi.spyOn(apiClient, "getConversation").mockRejectedValue(new Error("no such conversation"));
      const onBack = vi.fn();
      await show([], undefined, onBack);

      expect(await screen.findByRole("alert")).toHaveTextContent("Failed to load the conversation.");
      await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));
      expect(onBack).toHaveBeenCalledTimes(1);
    });

    it("has the way back while the session is still loading", async () => {
      serve(idle);
      vi.spyOn(apiClient, "getConversation").mockReturnValue(new Promise(() => {}));
      await show([], undefined, vi.fn());

      expect(screen.getByText("Loading conversation…")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "‹ Sessions" })).toBeInTheDocument();
    });
  });

  describe("the session's name", () => {
    it("is the title, else the first line, in the heading", async () => {
      serve([], makeConversationDetail({ title: null, first_line: "fix the bug" }));
      await show();

      expect(await screen.findByRole("heading", { name: "fix the bug" })).toBeInTheDocument();
    });

    it("renames, reads the session again and tells the page", async () => {
      const detail = makeConversationDetail({ first_line: "fix the bug" });
      serve([], detail);
      const rename = vi.spyOn(apiClient, "renameConversation").mockResolvedValue(makeConversation({ title: "Mine" }));
      const onChanged = vi.fn();
      await show([], onChanged);
      await userEvent.click(await screen.findByRole("button", { name: "Rename session" }));
      vi.mocked(apiClient.getConversation).mockResolvedValue({ ...detail, title: "Mine" });
      await userEvent.clear(screen.getByRole("textbox", { name: "Session name" }));
      await userEvent.type(screen.getByRole("textbox", { name: "Session name" }), "Mine{Enter}");

      expect(rename).toHaveBeenCalledExactlyOnceWith("c-1", CONVERSATION, "Mine");
      expect(await screen.findByRole("heading", { name: "Mine" })).toBeInTheDocument();
      expect(onChanged).toHaveBeenCalled();
    });
  });

  describe("Stop", () => {
    const stopButton = () => screen.queryByRole("button", { name: "Stop this reply" });

    it("is there only while a reply runs", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      expect(await screen.findByRole("button", { name: "Stop this reply" })).toBeEnabled();

      act(() => push(ev(3, "turn_end", "", 1)));
      expect(stopButton()).not.toBeInTheDocument();
    });

    it("is not there for an idle session", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "done"), ev(3, "turn_end", "", 1)]);
      await show();
      await screen.findByText("done");
      expect(stopButton()).not.toBeInTheDocument();
    });

    it("names the turn that is running, after the ones that ended", async () => {
      const interrupt = vi.spyOn(apiClient, "interruptConversation").mockResolvedValue(makeConversation());
      serve([ev(1, "user", "a"), ev(2, "assistant", "x"), ev(3, "turn_end", "", 1), ev(4, "user", "b"), ev(5, "assistant", "y")]);
      await show();
      await userEvent.click(await screen.findByRole("button", { name: "Stop this reply" }));

      expect(interrupt).toHaveBeenCalledWith("c-1", CONVERSATION, 2);
    });

    it("says Stopping… from the server's event, and is rebuilt from it after a reload", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial"), ev(3, "interrupt_requested", "", 1)]);
      await show();

      expect(await screen.findByRole("button", { name: "Stopping…" })).toBeDisabled();
      expect(screen.getAllByText("Stopping…").length).toBe(2);
      expect(screen.queryByText(/^Working…/)).not.toBeInTheDocument();
    });

    it("moves to Stopping… when the request's event arrives, then notes the stop when the turn ends", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      await screen.findByRole("button", { name: "Stop this reply" });

      act(() => push(ev(3, "interrupt_requested", "", 1)));
      expect(screen.getByRole("button", { name: "Stopping…" })).toBeDisabled();

      act(() => push(ev(4, "turn_end", "", 1)));
      act(() => push(ev(5, "interrupted", "", 1)));
      expect(screen.getByText("Stopped by you.")).toBeInTheDocument();
      expect(stopButton()).not.toBeInTheDocument();
      expect(screen.queryByText("Stopping…")).not.toBeInTheDocument();
    });

    it("shows no note when the turn ended before the stop landed", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      await screen.findByRole("button", { name: "Stop this reply" });

      act(() => push(ev(3, "turn_end", "", 1)));
      act(() => push(ev(4, "interrupt_requested", "", 1)));
      expect(screen.queryByText("Stopped by you.")).not.toBeInTheDocument();
      expect(screen.queryByText("Stopping…")).not.toBeInTheDocument();
    });

    it("does not show the next turn as stopping because an earlier one was", async () => {
      serve([ev(1, "user", "go"), ev(2, "interrupt_requested", "", 1), ev(3, "turn_end", "", 1), ev(4, "interrupted", "", 1), ev(5, "user", "again")]);
      await show();

      expect(await screen.findByRole("button", { name: "Stop this reply" })).toBeEnabled();
      expect(screen.getByText(/^Working…/)).toBeInTheDocument();
    });

    it("sends one request for repeated clicks before the event arrives", async () => {
      const interrupt = vi.spyOn(apiClient, "interruptConversation").mockResolvedValue(makeConversation());
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      await userEvent.click(await screen.findByRole("button", { name: "Stop this reply" }));
      await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "{Escape}{Escape}");

      expect(interrupt).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("button", { name: "Stopping…" })).toBeDisabled();
    });

    it("stops on Esc in the message box, keeping the draft", async () => {
      const interrupt = vi.spyOn(apiClient, "interruptConversation").mockResolvedValue(makeConversation());
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      const box = await screen.findByRole("textbox", { name: "Message" });
      await userEvent.type(box, "then this{Escape}");

      expect(interrupt).toHaveBeenCalledTimes(1);
      expect(box).toHaveValue("then this");
    });

    it("leaves Esc alone while the disconnect confirmation is open", async () => {
      const interrupt = vi.spyOn(apiClient, "interruptConversation").mockResolvedValue(makeConversation());
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      await userEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
      await screen.findByRole("alertdialog", { name: "Confirm disconnect" });
      await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "{Escape}");

      expect(interrupt).not.toHaveBeenCalled();
    });

    it("is disabled with the reason when the host is not running", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")], makeConversationDetail({ host_online: false, state: "ready" }));
      await show();

      expect(await screen.findByRole("button", { name: "Stop this reply" })).toBeDisabled();
      expect(screen.getAllByText("The host isn't running").length).toBeGreaterThan(0);
    });

    it("is disabled with the restart hint when the host is on older code", async () => {
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")], makeConversationDetail({ host_capabilities: [], state: "ready" }));
      await show();

      expect(await screen.findByRole("button", { name: "Stop this reply" })).toBeDisabled();
      expect(screen.getAllByText(/older version; restart it to enable Stop/).length).toBeGreaterThan(0);
    });

    it("reports a failed request in the notice and keeps the button", async () => {
      vi.spyOn(apiClient, "interruptConversation").mockRejectedValue(new ApiError(409, "that turn is not running"));
      serve([ev(1, "user", "go"), ev(2, "assistant", "partial")]);
      await show();
      await userEvent.click(await screen.findByRole("button", { name: "Stop this reply" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("that turn is not running");
      expect(screen.getByRole("button", { name: "Stop this reply" })).toBeEnabled();
    });
  });
});
