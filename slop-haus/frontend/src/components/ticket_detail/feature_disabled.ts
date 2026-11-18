import { ApiError } from "@/api";

// The server answers an attempt endpoint its env var switched off with a 404
// carrying this code. A bare 404 is a ticket or attempt that is gone, which
// the person should be told about rather than losing the controls.
export const FEATURE_DISABLED_CODE = "feature_disabled";

export function isFeatureDisabled(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404 && err.code === FEATURE_DISABLED_CODE;
}
