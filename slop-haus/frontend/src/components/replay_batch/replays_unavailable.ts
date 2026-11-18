import { ApiError } from "@/api";

// The server answers the replay endpoints with a 404 carrying this code when
// it does not serve replays; a bare 404 is something that is gone.
export function replaysUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404 && err.code === "feature_disabled";
}
