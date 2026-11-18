import { useContext, useState } from "react";
import { ErrorReporterContext } from "@slop-factory/logging/error_reporting/react";
import { ApiError } from "@/api";

// What to show when the task throws: a fixed fallback for anything that
// isn't the server's own words, or a function that decides -- returning
// null for a failure that isn't worth reporting (one a later request has
// already overtaken, say).
export type ActionFailure = string | ((err: unknown) => string | null);

export interface AsyncAction {
  busy: boolean;
  error: string | null;
  // For errors the caller finds before there's anything to run (a form
  // that fails its own checks), and for clearing one when the page moves on.
  setError: (error: string | null) => void;
  run: (task: () => Promise<void>, failure: ActionFailure) => Promise<void>;
}

// errorMessage is how every action reports a failure: an ApiError's detail
// is already written for a human, anything else gets the caller's fallback.
export function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
}

// useAsyncAction owns the busy flag and error of one button or form that
// sends a request: the error from the last attempt is cleared when the next
// one starts, the action is busy until the task has finished, and a failure
// is reported rather than thrown.
export function useAsyncAction(): AsyncAction {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Optional, unlike useErrorReporter: an action also runs where no reporter
  // is mounted (a test, a story).
  const reporter = useContext(ErrorReporterContext);

  async function run(task: () => Promise<void>, failure: ActionFailure) {
    setError(null);
    setBusy(true);
    try {
      await task();
    } catch (err) {
      // The action swallows the failure, so no global handler ever sees it.
      // An ApiError was already reported by the API client's failure
      // middleware (and a 4xx it filters out is an outcome the UI shows).
      if (!(err instanceof ApiError)) reporter?.reportError("action failed", err);
      const message = typeof failure === "string" ? errorMessage(err, failure) : failure(err);
      if (message !== null) setError(message);
    } finally {
      setBusy(false);
    }
  }

  return { busy, error, setError, run };
}
