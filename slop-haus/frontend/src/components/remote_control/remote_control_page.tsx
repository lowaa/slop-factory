import { useEffect, useMemo, useRef } from "react";
import type { Conductor } from "@/api";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { useNarrowLayout } from "@/lib/layout";
import ConversationView from "./conversation/conversation_view";
import NewSession from "./new_session";
import RemoteControlSetup from "./remote_control_setup";
import RunningHelp from "./running_help";
import type { RemoteControlView } from "./remote_control_view";
import SessionBar from "./session_bar";
import TenantRemoteControls from "./tenant_remote_controls";
import SessionsPanel from "./sessions_panel";
import { drafts, sessionDraftKey } from "./drafts";
import { findLiveSession } from "@/lib/live_session";
import { isUsable, useMachines } from "@/lib/use_machines";
import { usePanelOpen } from "./use_panel_open";
import "./remote_control_page.scss";

// What the address says the page shows. A session is on a machine. Without
// one the page opens the connected session if there is one, or else is a new
// session, on the machine named or the first there is. isNew asks for a new
// session even when one is connected. draft is the text for a message box:
// the session's, or the new session's. replace is for a selection the page
// makes on its own, which Back must not land on.
export interface RemoteControlSelection {
  conductorId?: string;
  sessionId?: string;
  draft?: string;
  isNew?: boolean;
  // A help page shown in place of a session, on desktop.
  view?: RemoteControlView;
  replace?: boolean;
}

interface RemoteControlPageProps extends RemoteControlSelection {
  onSelect: (selection: RemoteControlSelection) => void;
}

// RemoteControlPage drives the person's own Remote Control sessions (Claude
// Code or pi) from the browser: the machines they have registered, the sessions on them, and the
// one open. On desktop, setup and how to run one are pages the sessions panel
// opens in the main pane; on a phone they are closed sections under the list.
export default function RemoteControlPage({ conductorId, sessionId: addressSession, draft, isNew, view: addressView, onSelect }: RemoteControlPageProps) {
  const { state, reload } = useMachines();
  const [panelOpen, setPanelOpen] = usePanelOpen();
  // On a phone the list and the chat are screens of their own, told apart by
  // whether the address names a session: Back is then just the address
  // changing, and there is nothing of the other screen to scroll past.
  const narrow = useNarrowLayout();
  // A view names no session; it is ignored on a phone, which has no way to
  // open one.
  const view = narrow ? undefined : addressView;
  const sessionId = view ? undefined : addressSession;
  const chatScreen = narrow && !!sessionId;

  // The panel lists a disabled remote control to enable it again; nothing
  // else here may use one.
  const machines = useMemo(() => (state.status === "loaded" ? state.machines.filter(isUsable) : []), [state]);
  const usable = machines.map((m) => m.conductor);
  const owner = sessionId
    ? machines.find((m) => m.conductor.id === conductorId)?.conductor
    : (usable.find((c) => c.id === conductorId) ?? usable[0]);

  // Without a session in the address, the connected one is what the person
  // came for; starting a new one ends it.
  // Once New session is on show by default it stays, so a session connecting
  // elsewhere on a later refresh does not pull the person off a message they
  // are typing. Only once every machine's sessions are known: a read that
  // failed is not the same as there being none to open.
  const staysNew = useRef(false);
  const live = state.status === "loaded" && !sessionId && !isNew && !view && !staysNew.current ? findLiveSession(machines, conductorId) : undefined;
  const redirecting = live !== undefined;
  const sessionsKnown = machines.length > 0 && machines.every((m) => m.sessions !== null);
  const latchNew = state.status === "loaded" && !sessionId && !isNew && !view && !redirecting && sessionsKnown;
  useEffect(() => {
    staysNew.current = latchNew;
  }, [latchNew]);
  // What the open session's message box holds, so "+ New session" takes it along.
  const composerText = useRef("");
  // Held here rather than in the address: what was typed is the person's own
  // and does not belong in history or logs.
  const carried = useRef("");

  useEffect(() => {
    if (live)
      onSelect({
        conductorId: live.conductorId,
        sessionId: live.sessionId,
        draft: draft ?? (composerText.current || undefined),
        replace: true,
      });
    // onSelect is the caller's navigate; the selection is what matters.
  }, [live?.conductorId, live?.sessionId, draft]);

  // The session's message box has taken the draft; leaving it in the address
  // would put it back on a reload or a later visit.
  const sessionShown = state.status === "loaded" && !!sessionId && !!owner;
  useEffect(() => {
    if (sessionShown && draft) onSelect({ conductorId, sessionId, replace: true });
  }, [sessionShown, draft]);

  // isNew, so the connected session is not opened again at once. Like
  // "+ New session", what is in the message box stays with the person.
  const composerRows = narrow ? 2 : 3;
  const backToList = () => {
    onSelect({ conductorId: owner?.id ?? conductorId, isNew: true });
  };

  return (
    <UserSettingsProvider>
      <div className={`remote-control${chatScreen ? " remote-control--chat" : ""}`}>
        <div className="remote-control__body">
          {!chatScreen && (
            <SessionsPanel
              state={state}
              open={panelOpen}
              onToggle={setPanelOpen}
              sessionId={sessionId}
              screen={narrow}
              newOn={sessionId || view ? undefined : owner?.id}
              onAgentChanged={reload}
              view={view}
              onOpenView={narrow ? undefined : (next) => onSelect({ view: next })}
              onOpenSession={(conductor, session) => {
                carried.current = "";
                onSelect({ conductorId: conductor, sessionId: session });
              }}
              onNewSession={(conductor) => {
                carried.current = composerText.current;
                onSelect({ conductorId: conductor, isNew: true });
              }}
            />
          )}
          <main className="remote-control__main">
            {view === "setup" && <RemoteControlSetup asView onChanged={reload} />}
            {view === "running" && <RunningHelp asView />}
            {view === "tenant" && <TenantRemoteControls asView />}
            {chatScreen && !(state.status === "loaded" && owner) && <SessionBar title="Session" onBack={backToList} />}
            {state.status === "loading" && <p>Loading…</p>}
            {state.status === "error" && chatScreen && <p role="alert">{state.message}</p>}
            {state.status === "loaded" && sessionId && !owner && <p role="alert">That session is not on one of your remote controls.</p>}
            {state.status === "loaded" && sessionId && owner && (
              // Keyed by both ids, so a change of either starts a view of its
              // own and nothing read for the last one can sit on the next.
              <ConversationView
                key={`${owner.id}/${sessionId}`}
                conductor={owner}
                conversationId={sessionId}
                initialDraft={draft ?? drafts.get(sessionDraftKey(owner.id, sessionId))}
                focusDraft={!!draft}
                onDraftChange={(text) => {
                  composerText.current = text;
                  drafts.set(sessionDraftKey(owner.id, sessionId), text);
                }}
                onChanged={reload}
                onDeleted={backToList}
                onBack={chatScreen ? backToList : undefined}
                composerRows={composerRows}
              />
            )}
            {state.status === "loaded" && !sessionId && !view && owner && !redirecting && (
              <NewSession
                key={`${owner.id}/${draft ?? carried.current}`}
                composerRows={composerRows}
                machines={usable}
                conductorId={owner.id}
                draft={draft ?? carried.current}
                focusDraft={!!draft}
                onDraftChange={(text) => {
                  composerText.current = text;
                }}
                onPick={(id) => onSelect({ conductorId: id, draft, isNew: true })}
                onStarted={(conductor: Conductor, conversation) => {
                  reload();
                  carried.current = "";
                  onSelect({
                    conductorId: conductor.id,
                    sessionId: conversation.id,
                  });
                }}
              />
            )}
            {state.status === "loaded" && !sessionId && !view && !owner && (
              <p className="remote-control__empty">
                {narrow ? (
                  "Add a remote control above to start a session."
                ) : (
                  <>
                    To start a session, <button type="button" className="remote-control__link" onClick={() => onSelect({ view: "setup" })}>add a remote control</button>{" "}
                    or see <button type="button" className="remote-control__link" onClick={() => onSelect({ view: "running" })}>how to run one</button>.
                  </>
                )}
              </p>
            )}
          </main>
        </div>
        {narrow && !chatScreen && (
          <>
            <RemoteControlSetup onChanged={reload} />
            <RunningHelp />
            <TenantRemoteControls />
          </>
        )}
      </div>
    </UserSettingsProvider>
  );
}
