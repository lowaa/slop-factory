import { createContext, useContext, useEffect, useState, useSyncExternalStore } from "react";
import { Button } from "@slop-factory/ui";
import type { ChunkRecovery } from "./chunk_recovery";
import type { UpdateWatcher } from "./update_watcher";
import type { ErrorReporter } from "./error_reporter";
import "./app_update.scss";

export interface AppBuild {
  recovery: ChunkRecovery;
  // What the messages call the app, e.g. "slop-haus".
  appName: string;
}

export const AppBuildContext = createContext<AppBuild | null>(null);

const DEFAULT_APP_NAME = "This app";

// Tells the user a newer build is live. Unobtrusive and never reloads by
// itself: someone may be typing a ticket.
export function UpdateNotice({ watcher, appName }: { watcher: UpdateWatcher; appName: string }) {
  const tag = useSyncExternalStore(watcher.subscribe, watcher.getSnapshot);
  if (tag === undefined) return null;
  return (
    <div className="update-notice" role="status">
      <span className="update-notice__text">{appName} has been updated — reload to get the new version</span>
      <Button size="sm" variant="primary" onClick={() => window.location.reload()}>
        Reload
      </Button>
      <Button size="sm" variant="ghost" onClick={() => watcher.dismiss()}>
        Dismiss
      </Button>
    </div>
  );
}

interface ChunkLoadFallbackProps {
  // Reports the failure when given; the route's fallback has the reporter
  // in context, a panel boundary may not.
  reporter?: ErrorReporter | null;
  error: unknown;
  componentStack?: string | null;
}

// What a failed lazy chunk shows. It reloads the page once (ChunkRecovery
// decides whether that is still allowed); if it isn't, it asks the user to,
// rather than reporting "server error". A chunk that failed means the page
// could not render anyway, so a reload loses nothing the page was showing.
export function ChunkLoadFallback({ reporter, error, componentStack }: ChunkLoadFallbackProps) {
  const build = useContext(AppBuildContext);
  const appName = build?.appName ?? DEFAULT_APP_NAME;
  const [blocked, setBlocked] = useState(() => !(build?.recovery.canReload() ?? false));

  useEffect(() => {
    reporter?.reportError("chunk load failed", error, componentStack);
    // The attempt, not the render-time check, is what says whether a reload
    // is under way: it can refuse (no storage for the guard).
    setBlocked(build ? build.recovery.attempt() === "blocked" : true);
  }, [build, reporter, error, componentStack]);

  if (!blocked) return <p className="chunk-load-fallback" role="status">Updating…</p>;
  return (
    <div className="chunk-load-fallback" role="alert">
      <p>{appName} has been updated (or the connection dropped) — reload to continue.</p>
      <Button variant="primary" onClick={() => window.location.reload()}>
        Reload
      </Button>
    </div>
  );
}
