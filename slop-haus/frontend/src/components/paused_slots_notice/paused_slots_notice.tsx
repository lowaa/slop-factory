import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "@slop-factory/router";
import { apiClient, type SupervisorStatus } from "@/api";
import "./paused_slots_notice.scss";

interface PausedSlotsNoticeProps {
  // Changes whenever the board's own live refresh fires, so this is fetched
  // on the same stream rather than opening a second one.
  refreshToken: number;
}

// Says so on the board when a supervisor slot has sat paused past the
// server's bound. A paused slot takes no work, and nothing else on the board
// shows it: the queue just stops moving.
export default function PausedSlotsNotice({ refreshToken }: PausedSlotsNoticeProps) {
  const [status, setStatus] = useState<SupervisorStatus | null>(null);
  const latest = useRef(0);

  const load = useCallback(() => {
    const request = ++latest.current;
    return apiClient
      .getSupervisorStatus()
      .then((found) => {
        if (request === latest.current) setStatus(found);
      })
      .catch(() => {
        // Quiet: an aside to the board, which a failed read must not
        // replace. The next change tries again.
      });
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshToken]);

  const longPaused = status?.slots.filter((slot) => slot.paused_long) ?? [];
  if (longPaused.length === 0) return null;
  const noun = longPaused.length === 1 ? "slot is" : "slots are";
  return (
    <section className="paused-slots-notice" role="status" aria-label="Paused slots">
      <p className="paused-slots-notice__text">
        {longPaused.length} supervisor {noun} paused and not taking work. See the <Link to="/workers">Workers page</Link> for why.
      </p>
    </section>
  );
}
