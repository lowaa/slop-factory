import { useCallback, useRef } from "react";

// useLatestRequest lets a component act only on the answer to the request
// it made last. Each call to begin() supersedes every earlier one, and
// returns a check for whether this one still is the latest -- responses
// can arrive out of order, and an older one landing after a newer one
// would put stale data back on screen. Calling begin() without a request
// just drops whatever is in flight.
export function useLatestRequest(): () => () => boolean {
  const seq = useRef(0);
  return useCallback(() => {
    const mine = ++seq.current;
    return () => mine === seq.current;
  }, []);
}
