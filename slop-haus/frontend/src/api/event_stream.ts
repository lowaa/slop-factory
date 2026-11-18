// The app's wrapper over the browser's EventSource: every server
// stream the app opens (the board's changes) goes through it, so the
// session handling below is written once.
//
// The browser retries a dropped stream by itself, but not one refused
// outright: an EventSource that gets a 401 closes for good. That is what
// happens on the first reconnect after the access token expires, so a
// closed stream refreshes the session and opens a new one. A refresh that
// fails means the session is over, and is announced as a sign-out.

// What a stream needs of the session: @slop-factory/auth's SessionClient.
export interface StreamSession {
  generation(): number;
  refresh(): Promise<boolean>;
  announceSignedOut(generation: number): void;
}

export interface EventStreamOptions {
  // url is asked again each time the stream is opened, so a reopen after a
  // refresh resumes from whatever cursor the caller has reached by then.
  url: () => string;
  // onData is handed each event's data, parsed as JSON. Data that isn't
  // JSON is dropped rather than thrown into the page; checking the shape is
  // the caller's, as only it knows what the stream sends.
  onData: (data: unknown) => void;
  // How long to wait before reopening a stream that was refused again
  // without having delivered anything since its last reopen. A refusal that
  // a refresh does not cure (the page is in a tenant the session has left)
  // would otherwise reopen in a tight loop until the page is replaced.
  retryDelayMs?: number;
}

// openEventStream opens the stream and returns the function that closes
// it for good; callers must call it (from a React effect's cleanup, say).
export function openEventStream(
  session: StreamSession,
  { url, onData, retryDelayMs = 3000 }: EventStreamOptions,
): () => void {
  let source: EventSource | null = null;
  let stopped = false;
  let refusedInARow = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;

  const open = () => {
    source = new EventSource(url());
    source.onmessage = (event: MessageEvent<string>) => {
      let data: unknown;
      try {
        data = JSON.parse(event.data);
      } catch {
        return;
      }
      refusedInARow = 0;
      onData(data);
    };
    source.onerror = () => {
      if (stopped || source?.readyState !== EventSource.CLOSED) return;
      const generation = session.generation();
      void session.refresh().then((ok) => {
        if (stopped) return;
        if (ok) {
          refusedInARow++;
          if (refusedInARow > 1) retryTimer = setTimeout(open, retryDelayMs);
          else open();
        } else session.announceSignedOut(generation);
      });
    };
  };
  open();

  return () => {
    stopped = true;
    clearTimeout(retryTimer);
    source?.close();
  };
}
