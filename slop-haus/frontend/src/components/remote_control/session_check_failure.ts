import { ApiError } from "@/api";

// The two reads the switch guard makes, named in the message so the person
// (and whoever they report it to) can tell which one failed.
export type CheckStep = "list" | "read";

const STEP_LABEL: Record<CheckStep, string> = {
  list: "list this machine's sessions",
  read: "read the other session's transcript",
};

// A deploy briefly answers with a gateway error, and a dropped connection
// surfaces from fetch as a TypeError; both usually pass within seconds.
const GATEWAY_STATUSES = new Set([502, 503, 504]);

// Mutable so tests need not wait out the production pause.
export const checkRetry = { delayMs: 1000 };

export function isTransientFailure(err: unknown): boolean {
  if (err instanceof ApiError) return GATEWAY_STATUSES.has(err.status);
  return err instanceof TypeError;
}

export class CheckStepError extends Error {
  readonly step: CheckStep;
  readonly cause: unknown;

  constructor(step: CheckStep, cause: unknown) {
    super(`could not ${STEP_LABEL[step]}`);
    this.name = "CheckStepError";
    this.step = step;
    this.cause = cause;
  }
}

function reason(err: unknown): string {
  if (err instanceof ApiError) {
    if (GATEWAY_STATUSES.has(err.status)) return `the server is unavailable or restarting (HTTP ${err.status})`;
    return `${err.message} (HTTP ${err.status})`;
  }
  if (err instanceof TypeError) {
    return typeof navigator !== "undefined" && !navigator.onLine
      ? "you appear to be offline"
      : "the request did not reach the server";
  }
  // Typically a non-JSON body from a gateway or a stale page bundle.
  return "the server's reply was not understood";
}

export function describeCheckFailure(step: CheckStep, err: unknown): string {
  return `Could not ${STEP_LABEL[step]}: ${reason(err)}.`;
}

// Runs one step, trying once more after a pause if the failure looks like a
// rollout or a network blip, and tags a final failure with the step.
export async function runCheckStep<T>(step: CheckStep, task: () => Promise<T>): Promise<T> {
  try {
    return await task();
  } catch (first) {
    if (!isTransientFailure(first)) throw new CheckStepError(step, first);
    await new Promise((resolve) => setTimeout(resolve, checkRetry.delayMs));
    try {
      return await task();
    } catch (second) {
      throw new CheckStepError(step, second);
    }
  }
}
