import { openEventStream } from "./event_stream";
import {
  BASE_URL,
  withTenant,
  setupScriptFrom,
  session,
  client,
  unwrap,
} from "./client";
import type {
  AgentMessage,
  BoardChange,
  Conductor,
  ConductorDirectoryEntry,
  ConductorHost,
  ConductorList,
  Conversation,
  ConversationDetail,
  ConversationEvent,
  ConversationEventsPage,
  SetupScript,
} from "./types";

export const conversationsApi = {
  // -- Remote controls (conductors in the API) --
  //
  // A person's alone: no agent token reaches these routes.

  async conductorDirectory(opts: { includeArchived?: boolean } = {}): Promise<ConductorDirectoryEntry[]> {
    const body = await unwrap(
      client.GET("/api/conductors/directory", {
        params: { query: { include_archived: opts.includeArchived } },
      }),
    );
    return body.conductors;
  },

  async listConductors(opts: { includeArchived?: boolean } = {}): Promise<ConductorList> {
    const body = await unwrap(
      client.GET("/api/conductors", {
        params: { query: { include_archived: opts.includeArchived } },
      }),
    );
    return { own: body.conductors, others: body.others };
  },

  // registerConductor makes a conductor owned by the signed-in person and
  // returns its setup script, as createAgentSetupScript does for an agent.
  // The owner is the session's, never a field of the request. A stale
  // sign-in is a 403 reauth_required.
  async registerConductor(name: string): Promise<SetupScript> {
    return setupScriptFrom(
      client.POST("/api/conductors", { body: { name }, parseAs: "text" }),
    );
  },

  // rotateConductorSetupScript signs out every workspace acting as the
  // conductor and returns a new setup script. Its owner only.
  async rotateConductorSetupScript(id: string): Promise<SetupScript> {
    return setupScriptFrom(
      client.POST("/api/conductors/{id}/setup_script", {
        params: { path: { id } },
        parseAs: "text",
      }),
    );
  },

  async setConductorDisabled(
    id: string,
    disabled: boolean,
  ): Promise<Conductor> {
    return unwrap(
      client.PATCH("/api/conductors/{id}", {
        params: { path: { id } },
        body: { disabled },
      }),
    );
  },

  async archiveConductor(id: string): Promise<Conductor> {
    return unwrap(client.POST("/api/conductors/{id}/archive", { params: { path: { id } } }));
  },

  async unarchiveConductor(id: string): Promise<Conductor> {
    return unwrap(client.POST("/api/conductors/{id}/unarchive", { params: { path: { id } } }));
  },

  async getConductorHost(conductorId: string): Promise<ConductorHost> {
    return unwrap(
      client.GET("/api/conductors/{id}/host", {
        params: { path: { id: conductorId } },
      }),
    );
  },

  async listConversations(conductorId: string): Promise<Conversation[]> {
    const body = await unwrap(
      client.GET("/api/conductors/{id}/conversations", {
        params: { path: { id: conductorId } },
      }),
    );
    return body.conversations;
  },

  // startConversation begins one on the remote control, replacing its live
  // one. clientId makes a start repeated after a lost reply return the
  // conversation the first made.
  async startConversation(
    conductorId: string,
    start: { message?: string; clientId: string },
  ): Promise<Conversation> {
    return unwrap(
      client.POST("/api/conductors/{id}/conversations", {
        params: { path: { id: conductorId } },
        body: { message: start.message, client_id: start.clientId },
      }),
    );
  },

  async getConversation(
    conductorId: string,
    id: string,
  ): Promise<ConversationDetail> {
    return unwrap(
      client.GET("/api/conductors/{id}/conversations/{conversation_id}", {
        params: { path: { id: conductorId, conversation_id: id } },
      }),
    );
  },

  // renameConversation sets the name its owner gave a session. A null or
  // blank title clears it, and the first line of its first message shows again.
  async renameConversation(
    conductorId: string,
    id: string,
    title: string | null,
  ): Promise<Conversation> {
    return unwrap(
      client.PATCH("/api/conductors/{id}/conversations/{conversation_id}", {
        params: { path: { id: conductorId, conversation_id: id } },
        body: { title },
      }),
    );
  },

  // deleteConversation removes a session and its transcript for good. The
  // server refuses (409) while the session is connected.
  async deleteConversation(conductorId: string, id: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/conductors/{id}/conversations/{conversation_id}", {
        params: { path: { id: conductorId, conversation_id: id } },
      }),
    );
  },

  // listRecentConversationEvents reads a transcript backwards by turns: the
  // most recent `turns` turns before `before` (0 for after the newest), whole.
  // The first read (no before) also brings the owner's recent prompts.
  async listRecentConversationEvents(
    conductorId: string,
    id: string,
    { before, turns }: { before: number; turns: number },
  ): Promise<ConversationEventsPage> {
    const body = await unwrap(
      client.GET(
        "/api/conductors/{id}/conversations/{conversation_id}/events",
        {
          params: {
            path: { id: conductorId, conversation_id: id },
            query: { before, turns },
          },
        },
      ),
    );
    return {
      events: body.events,
      hasMore: body.has_more ?? false,
      nextBefore: body.next_before ?? 0,
      prompts: body.prompts ?? null,
    };
  },

  // A message to a conversation with no process reconnects it first, resuming
  // its Claude session and replacing the live one.
  async sendConversationMessage(
    conductorId: string,
    id: string,
    message: { text: string; clientId: string; attachmentIds?: string[] },
  ): Promise<ConversationEvent> {
    return unwrap(
      client.POST(
        "/api/conductors/{id}/conversations/{conversation_id}/messages",
        {
          params: { path: { id: conductorId, conversation_id: id } },
          body: {
            text: message.text,
            client_id: message.clientId,
            ...(message.attachmentIds?.length ? { attachment_ids: message.attachmentIds } : {}),
          },
        },
      ),
    );
  },

  // disconnectConversation ends the session's Claude process, a turn in
  // progress included, and keeps the session: the next message reconnects it.
  async disconnectConversation(
    conductorId: string,
    id: string,
  ): Promise<Conversation> {
    return unwrap(
      client.POST(
        "/api/conductors/{id}/conversations/{conversation_id}/disconnect",
        {
          params: { path: { id: conductorId, conversation_id: id } },
        },
      ),
    );
  },

  // interruptConversation stops the reply in progress and keeps the process and
  // the session. It names the turn, so a request that arrives after that turn
  // ended cannot stop the next one; the server refuses it with a 409.
  async interruptConversation(
    conductorId: string,
    id: string,
    turn: number,
  ): Promise<Conversation> {
    return unwrap(
      client.POST(
        "/api/conductors/{id}/conversations/{conversation_id}/interrupt",
        {
          params: { path: { id: conductorId, conversation_id: id } },
          body: { turn },
        },
      ),
    );
  },

  // clearConversation starts the session's context over, keeping the session.
  // A reply in progress is refused (409) unless interrupt is true, which stops
  // it first.
  async clearConversation(
    conductorId: string,
    id: string,
    opts: { interrupt?: boolean } = {},
  ): Promise<Conversation> {
    return unwrap(
      client.POST("/api/conductors/{id}/conversations/{conversation_id}/clear", {
        params: { path: { id: conductorId, conversation_id: id } },
        body: { interrupt: opts.interrupt },
      }),
    );
  },

  // compactConversation summarises the session's context. Refused (409) when
  // the session's harness cannot compact, and as clearConversation is while a
  // reply runs.
  async compactConversation(
    conductorId: string,
    id: string,
    opts: { interrupt?: boolean } = {},
  ): Promise<Conversation> {
    return unwrap(
      client.POST("/api/conductors/{id}/conversations/{conversation_id}/compact", {
        params: { path: { id: conductorId, conversation_id: id } },
        body: { interrupt: opts.interrupt },
      }),
    );
  },

  // streamConversation opens the conversation's transcript stream through
  // openEventStream, as streamBoardChanges does the board's, calling onEvent
  // with each event after `after`. A reopen resumes from the last event seen.
  streamConversation(
    conductorId: string,
    id: string,
    after: number,
    onEvent: (event: ConversationEvent) => void,
  ): () => void {
    let cursor = after;
    return openEventStream(session, {
      url: () =>
        withTenant(
          `${BASE_URL}/api/conductors/${conductorId}/conversations/${id}/stream?after=${cursor}`,
        ),
      onData: (data) => {
        const event = data as ConversationEvent | null;
        if (
          typeof event?.seq !== "number" ||
          typeof event.kind !== "string" ||
          typeof event.body !== "string"
        )
          return;
        cursor = Math.max(cursor, event.seq);
        onEvent(event);
      },
    });
  },

  // -- Messages to agents --
  //
  // A person's alone, like proposals: no agent token reaches these routes.

  async sendAgentMessage(agentId: string, body: string): Promise<AgentMessage> {
    return await unwrap(
      client.POST("/api/agents/{id}/messages", {
        params: { path: { id: agentId } },
        body: { body },
      }),
    );
  },

  async listAgentMessages(): Promise<AgentMessage[]> {
    return (await unwrap(client.GET("/api/agent_messages"))).messages;
  },

  // streamBoardChanges opens the server's board stream through
  // openEventStream (./event_stream), which reopens one the server refused
  // once the session is refreshed. It lives here, like every other call, so
  // component tests can vi.spyOn it instead of needing a jsdom EventSource.
  // It returns the function that closes the stream; callers must call it
  // (e.g. from a React effect's cleanup).
  //
  // streamBoardChanges calls onChange with the board's new revision each
  // time anything on the board changes, with the event itself as the second
  // argument: it names the changed tickets, to be applied in place, or says
  // to refetch the list. With
  // no `since` the first event is the current revision, so the caller has
  // one to pass when it reopens the stream later and isn't sent a change
  // it has already seen.
  streamBoardChanges(
    onChange: (revision: number, change: BoardChange) => void,
    since?: number,
  ): () => void {
    let cursor = since ?? 0;
    return openEventStream(session, {
      url: () =>
        withTenant(
          `${BASE_URL}/api/tickets/stream${cursor > 0 ? `?since=${cursor}` : ""}`,
        ),
      onData: (data) => {
        const change = data as BoardChange | null;
        if (typeof change?.revision !== "number") return;
        cursor = change.revision;
        onChange(change.revision, change);
      },
    });
  },
};
