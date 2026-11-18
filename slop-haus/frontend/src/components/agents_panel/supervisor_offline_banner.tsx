import type { SupervisorStatus } from "@/api";
import "./supervisor_offline_banner.scss";

type Props = { status: SupervisorStatus };

export default function SupervisorOfflineBanner({ status }: Props) {
  const polled = status.last_polled_at ? new Date(status.last_polled_at).toLocaleString() : "unknown";
  const noun = status.claimable === 1 ? "ticket" : "tickets";
  return (
    <p className="supervisor-offline-banner" role="alert">
      Supervisor offline: last polled {polled}, {status.claimable} {noun} claimable
    </p>
  );
}
