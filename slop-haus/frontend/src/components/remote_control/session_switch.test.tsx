import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError, apiClient, REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type ConversationEvent } from "@/api";
import {
  makeConductor,
  makeConversation,
  makeConversationDetail,
  makeConversationEvent,
} from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { ProposalsContext } from "@/components/proposals";
import ConversationView from "./conversation/conversation_view";
import NewSession from "./new_session";
import { checkRetry } from "./session_check_failure";
import { recentEventsOf } from "@/test/recent_events";

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const LIVE = "55555555-5555-4555-8555-555555555555";
const STOPPED = "66666666-6666-4666-8666-666666666666";

function ev(seq: number, kind: ConversationEvent["kind"], body: string, turn: number | null = null) {
  return makeConversationEvent({ seq, kind, body, turn });
}

const MID_REPLY = [ev(1, "user", "Refactor the billing module"), ev(2, "assistant", "Looking at it", 1)];
const IDLE = [ev(1, "user", "hi"), ev(2, "assistant", "hello", 1), ev(3, "turn_end", "", 1)];

// The machine has LIVE running with the given transcript and STOPPED, which is
// the one open or about to be started over.
function serve(liveHistory: ConversationEvent[], explained: boolean) {
  vi.spyOn(apiClient, "listConversations").mockResolvedValue([
    makeConversation({ id: LIVE, state: "ready", first_line: liveHistory.find((e) => e.kind === "user")?.body ?? null }),
    makeConversation({ id: STOPPED, state: "stopped" }),
  ]);
  vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(async (c, id, range) =>
    recentEventsOf(id === LIVE ? liveHistory : [])(c, id, range),
  );
  vi.spyOn(apiClient, "getConversation").mockImplementation(async (_c, id) =>
    makeConversationDetail({ id, state: id === LIVE ? "ready" : "stopped" }),
  );
  vi.spyOn(apiClient, "streamConversation").mockReturnValue(() => {});
  vi.spyOn(apiClient, "listMySettings").mockResolvedValue(
    explained
      ? [{ name: REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type: "bool", bool_val: true, is_default: false } as never]
      : [],
  );
  vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue({} as never);
  vi.spyOn(apiClient, "sendConversationMessage").mockResolvedValue(ev(9, "user", "go"));
  vi.spyOn(apiClient, "startConversation").mockResolvedValue(makeConversation({ id: "new" }));
}

async function openStopped() {
  await renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
      <UserSettingsProvider>
        <ConversationView conductor={conductor} conversationId={STOPPED} />
      </UserSettingsProvider>
    </ProposalsContext.Provider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  // The saved preference is read as the page loads.
  await waitFor(() => expect(apiClient.listMySettings).toHaveBeenCalled());
}

async function sendFromStopped(text: string) {
  await userEvent.type(screen.getByRole("textbox", { name: "Message" }), text);
  await userEvent.click(screen.getByRole("button", { name: "Send" }));
}

describe("switching sessions", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    checkRetry.delayMs = 0;
  });

  it("asks before a send would stop a session that is mid-reply, naming it", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    await sendFromStopped("continue please");

    expect(await screen.findByRole("alertdialog", { name: "Confirm switch" })).toHaveTextContent(
      'Session "Refactor the billing module" is replying. Stop it and continue here?',
    );
    expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
  });

  it("reads only the other session's latest turn to see whether it is replying", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    const read = vi.mocked(apiClient.listRecentConversationEvents);
    read.mockClear();
    await sendFromStopped("continue please");
    await screen.findByRole("alertdialog", { name: "Confirm switch" });

    expect(read.mock.calls.filter((c) => c[1] === LIVE).map((c) => c[2])).toEqual([{ before: 0, turns: 1 }]);
  });

  it("Keep it sends nothing and leaves the draft in the box", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    await sendFromStopped("continue please");
    await userEvent.click(await screen.findByRole("button", { name: "Keep it" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("continue please");
  });

  it("Stop it and continue sends the message", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    await sendFromStopped("continue please");
    await userEvent.click(await screen.findByRole("button", { name: "Stop it and continue" }));

    await waitFor(() =>
      expect(apiClient.sendConversationMessage).toHaveBeenCalledWith("c-1", STOPPED, expect.objectContaining({ text: "continue please" })),
    );
  });

  it("does not ask when taking over an idle session", async () => {
    serve(IDLE, true);
    await openStopped();
    await sendFromStopped("continue please");

    await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("asks before a new session would stop one that is mid-reply", async () => {
    serve(MID_REPLY, true);
    await renderSignedIn(
      <UserSettingsProvider>
        <NewSession machines={[conductor]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />
      </UserSettingsProvider>,
    );
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "start fresh");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alertdialog", { name: "Confirm switch" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(apiClient.startConversation).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("start fresh");
  });

  it("is always shown for a mid-reply session, with no way to turn it off", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    await sendFromStopped("one");
    expect(await screen.findByRole("alertdialog", { name: "Confirm switch" })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: "Do not show again" })).not.toBeInTheDocument();
  });

  describe("the explanation", () => {
    it("shows on the first switch, as its own dialog when nothing is mid-reply", async () => {
      serve(IDLE, false);
      await openStopped();
      await sendFromStopped("go");

      const dialog = await screen.findByRole("alertdialog", { name: "Switching sessions" });
      expect(dialog).toHaveTextContent("Carried over");
      expect(dialog).toHaveTextContent("Not carried over");
      expect(dialog).toHaveTextContent("What happens");
      expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
      await userEvent.click(screen.getByRole("button", { name: "Continue" }));
      await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
      expect(apiClient.setMyBoolSetting).not.toHaveBeenCalled();
    });

    it("saves Do not show again, and is not shown when it was saved before", async () => {
      serve(IDLE, false);
      await openStopped();
      await sendFromStopped("go");
      await userEvent.click(await screen.findByRole("checkbox", { name: "Do not show again" }));
      await userEvent.click(screen.getByRole("button", { name: "Continue" }));
      expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith(REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, true);
      await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalledTimes(1));

      // The same mounted view, without waiting for a re-read.
      await sendFromStopped("again");
      await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalledTimes(2));
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    it("is not shown once it has been dismissed", async () => {
      serve(IDLE, true);
      await openStopped();
      await sendFromStopped("go");

      await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    it("is not shown for a new session on a machine with no other session", async () => {
      serve(IDLE, false);
      vi.spyOn(apiClient, "listConversations").mockResolvedValue([]);
      await renderSignedIn(
      <UserSettingsProvider>
        <NewSession machines={[conductor]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />
      </UserSettingsProvider>,
    );
      await userEvent.click(screen.getByRole("button", { name: "Send" }));

      await waitFor(() => expect(apiClient.startConversation).toHaveBeenCalled());
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    it("cancelling leaves the draft and sends nothing", async () => {
      serve(IDLE, false);
      await openStopped();
      await sendFromStopped("go");
      await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));

      expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("go");
    });
  });

  it("is one dialog, the confirmation with the explanation inside, when both apply", async () => {
    serve(MID_REPLY, false);
    await openStopped();
    await sendFromStopped("go");

    const dialog = await screen.findByRole("alertdialog", { name: "Confirm switch" });
    expect(screen.getAllByRole("alertdialog")).toHaveLength(1);
    expect(dialog).toHaveTextContent("Not carried over");
    await userEvent.click(screen.getByRole("checkbox", { name: "Do not show again" }));
    await userEvent.click(screen.getByRole("button", { name: "Stop it and continue" }));

    await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith(REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, true);
  });

  it("sends what the box holds when the question is answered, not what it held when it was asked", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    await sendFromStopped("first words");
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), " and more");
    await userEvent.click(await screen.findByRole("button", { name: "Stop it and continue" }));

    await waitFor(() =>
      expect(apiClient.sendConversationMessage).toHaveBeenCalledWith("c-1", STOPPED, expect.objectContaining({ text: "first words and more" })),
    );
  });

  it("asks when the open session looked live but has lost its process to another session", async () => {
    serve(MID_REPLY, true);
    // The page last read STOPPED as ready; the server now says another has it.
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail({ id: STOPPED, state: "ready" }));
    await renderSignedIn(
      <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
        <UserSettingsProvider>
          <ConversationView conductor={conductor} conversationId={STOPPED} />
        </UserSettingsProvider>
      </ProposalsContext.Provider>,
    );
    await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "hi");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alertdialog", { name: "Confirm switch" })).toBeInTheDocument();
    expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
  });

  it("asks nothing when sending in the live session itself", async () => {
    serve(MID_REPLY, false);
    await renderSignedIn(
      <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
        <UserSettingsProvider>
          <ConversationView conductor={conductor} conversationId={LIVE} />
        </UserSettingsProvider>
      </ProposalsContext.Provider>,
    );
    await screen.findByRole("textbox", { name: "Message" });
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "more");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("says which step failed and why, and sends nothing", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    vi.spyOn(apiClient, "listConversations").mockRejectedValue(new ApiError(500, "boom"));
    await sendFromStopped("go");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not list this machine's sessions: boom (HTTP 500).",
    );
    expect(apiClient.sendConversationMessage).not.toHaveBeenCalled();
  });

  it("names the transcript read when that is the step that failed, after retrying it once", async () => {
    serve(MID_REPLY, true);
    await openStopped();
    const read = vi.spyOn(apiClient, "listRecentConversationEvents").mockRejectedValue(new ApiError(503, "down"));
    await sendFromStopped("go");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not read the other session's transcript: the server is unavailable or restarting (HTTP 503).",
    );
    expect(read.mock.calls.filter((c) => c[1] === LIVE)).toHaveLength(2);
  });

  it("carries on when the one retry succeeds", async () => {
    serve(IDLE, true);
    await openStopped();
    vi.spyOn(apiClient, "listConversations")
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValue([makeConversation({ id: STOPPED, state: "stopped" })]);
    await sendFromStopped("go");

    await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("lets the person dismiss the error or run the check again", async () => {
    serve(IDLE, true);
    await openStopped();
    const list = vi.spyOn(apiClient, "listConversations").mockRejectedValue(new ApiError(500, "boom"));
    await sendFromStopped("go");
    await screen.findByRole("alert");

    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByRole("alert");
    list.mockResolvedValue([makeConversation({ id: STOPPED, state: "stopped" })]);
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(apiClient.sendConversationMessage).toHaveBeenCalled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
