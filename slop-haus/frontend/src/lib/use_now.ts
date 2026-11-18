import { useEffect, useState } from "react";

// useNow is the current time in ms, re-read every intervalMs so a label
// built from it (a countdown, a window that closes) updates without
// waiting for something else to re-render.
export function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}
