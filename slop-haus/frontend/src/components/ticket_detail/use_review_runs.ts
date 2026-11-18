import { apiClient } from "@/api";
import { resourceKeys, useResource } from "@/lib/resource";

// Read afresh each time the section opens: a run lands while the page is
// closed, and a cached list would be shown as current.
export function useReviewRuns(ticketId: number, attempt: number) {
  return useResource(resourceKeys.ticketReviewRuns(ticketId, attempt), () => apiClient.listAttemptReviewRuns(ticketId, attempt), {
    keepUnused: false,
    fallbackError: "Failed to load the review runs.",
  });
}
