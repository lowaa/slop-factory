import "./timestamp.scss";

// Minute precision is enough to place a comment among its
// neighbours; seconds only add noise to every line. The date stays in
// because a board is left open for days, so "9:02 AM" alone
// would be ambiguous.
const minuteFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

interface TimestampProps {
  // An RFC 3339 instant, as the API sends every *_at field.
  at: string;
}

// The exact instant stays on the element for machines, and to the second
// in the tooltip for anyone who needs to line two events up.
export default function Timestamp({ at }: TimestampProps) {
  const date = new Date(at);
  if (Number.isNaN(date.getTime())) return null;
  return (
    <time className="timestamp" dateTime={at} title={date.toLocaleString()}>
      {minuteFormat.format(date)}
    </time>
  );
}
