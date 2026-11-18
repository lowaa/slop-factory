import { useEffect, useState } from "react";
import { apiClient, type OnboardingStatus } from "@/api";

// null until read, and after a failed read: this is an aside to the board and
// the ticket form, which a lookup that failed must not replace or block.
export function useOnboardingStatus(refreshToken = 0): OnboardingStatus | null {
  const [status, setStatus] = useState<OnboardingStatus | null>(null);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .getOnboardingStatus()
      .then((found) => {
        if (!cancelled) setStatus(found);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [refreshToken]);
  return status;
}
