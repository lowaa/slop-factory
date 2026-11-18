import { beforeEach, vi } from "vitest";
import { apiClient, ApiError } from "@/api";
import { makeHarnessConfig } from "@/test/fixtures";
import { mockApi } from "@/test/mock_api";

// stubSideLists stands in for the two lists the panel fetches alongside
// the roster itself -- grants and agent roles -- with an empty default a
// test can override per-list when it cares what's in one of them.
export function stubSideLists() {
  mockApi({ listGrants: [], listAgentRoles: [] });
}

// stubPanelDefaults registers the stubs every render of the panel needs: the
// harness config section loads each time, so tests that care what is in it
// override this.
export function stubPanelDefaults() {
  beforeEach(() => {
    mockApi({
      getHarnessConfig: makeHarnessConfig(),
      listAgentMessages: [],
      getSupervisorStatus: { offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 },
    });
    // Not an owner unless a test says so.
    vi.spyOn(apiClient, "listVariants").mockRejectedValue(new ApiError(403, "owners only"));
    vi.spyOn(apiClient, "streamBoardChanges").mockReturnValue(() => {});
  });
}
