interface RecheckAtProps {
  at: Date;
  // How many back-to-back cooldown releases the ticket has had.
  check?: number;
}

// Local time of day: "re-check after 14:30" is read against the viewer's
// clock. The full timestamp, with its zone, is on hover.
export default function RecheckAt({ at, check }: RecheckAtProps) {
  const time = at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return (
    <time dateTime={at.toISOString()} title={at.toLocaleString()}>
      re-check after {time}
      {check ? ` (check ${check})` : ""}
    </time>
  );
}
