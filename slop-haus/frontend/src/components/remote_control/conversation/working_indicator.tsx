import { useEffect, useState } from "react";
import type { ActivityCall } from "@/domain/transcript";

function elapsedLabel(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

interface Props {
  // When the turn began (the owner's message), as the server stamped it.
  since: string | null;
  activity: ActivityCall | null;
  // The owner asked to stop the turn: it is ending, whatever it was doing.
  stopping?: boolean;
}

export default function WorkingIndicator({ since, activity, stopping = false }: Props) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const start = since === null ? NaN : Date.parse(since);
  const elapsed = Number.isNaN(start) ? "" : ` ${elapsedLabel((now - start) / 1000)}`;
  if (stopping) {
    return (
      <li className="conversation__note">
        <span className="conversation__replying" role="status">
          <span className="conversation__spinner" aria-hidden="true" />
          Stopping…
        </span>
      </li>
    );
  }
  return (
    <li className="conversation__note conversation__replying">
      <span className="conversation__spinner" aria-hidden="true" />
      <span role="status">
        Working…{elapsed}
        {activity && (
          <span className="conversation__working-activity">
            {" "}
            {activity.tool}
            {activity.line !== "" && `: ${activity.line}`}
          </span>
        )}
      </span>
    </li>
  );
}
