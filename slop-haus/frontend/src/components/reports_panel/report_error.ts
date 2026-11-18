import { ApiError } from "@/api";

// A report that times out comes back as a 503, and the useful advice then is
// a shorter period rather than the server's generic detail.
export function reportErrorMessage(what: string, fallback: string): (err: unknown) => string {
  return (err) => {
    if (err instanceof ApiError) return err.status === 503 ? `The ${what} took too long to compute. Try a shorter period.` : err.message;
    return fallback;
  };
}
