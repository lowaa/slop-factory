import { Button } from "@slop-factory/ui";
import type { SessionSwitch } from "./use_session_switch";

// The guard's failure, with a way out of it: the check can be run again, or
// the message put away, rather than sitting there until the next send.
export default function SessionSwitchError({ sessionSwitch, className }: { sessionSwitch: SessionSwitch; className: string }) {
  if (sessionSwitch.error === null) return null;
  return (
    <p role="alert" className={className}>
      {sessionSwitch.error}{" "}
      <Button onClick={sessionSwitch.retry}>Try again</Button>{" "}
      <Button onClick={sessionSwitch.dismiss}>Dismiss</Button>
    </p>
  );
}
