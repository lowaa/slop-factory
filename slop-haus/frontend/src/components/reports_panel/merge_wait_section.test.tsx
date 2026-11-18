import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeLockHistoryReport } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import MergeWaitSection from "./merge_wait_section";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("MergeWaitSection", () => {
  it("asks for 30 days at most, and says so, when the period is 90", async () => {
    const spy = vi.spyOn(apiClient, "getLockHistoryReport").mockResolvedValue(makeLockHistoryReport());

    await renderSignedIn(<MergeWaitSection days={90} />);

    expect(await screen.findByTestId("merge-wait-capped")).toBeVisible();
    expect(spy).toHaveBeenCalledWith({ days: 30 });
  });

  it("says so when the lock was not taken", async () => {
    vi.spyOn(apiClient, "getLockHistoryReport").mockResolvedValue(makeLockHistoryReport());

    await renderSignedIn(<MergeWaitSection days={7} />);

    expect(await screen.findByTestId("merge-wait-none")).toBeVisible();
    expect(screen.queryByTestId("merge-wait-capped")).toBeNull();
  });

  it("shows the wait and hold figures", async () => {
    const total = {
      day: null,
      holds: 4,
      ongoing: 1,
      queued: 3,
      wait: { p50: 120, p95: 600 },
      hold: { p50: 60, p95: 300 },
    };
    vi.spyOn(apiClient, "getLockHistoryReport").mockResolvedValue(makeLockHistoryReport({ total, held_seconds: 900 }));

    await renderSignedIn(<MergeWaitSection days={7} />);

    expect(await screen.findByText("3 of 4")).toBeVisible();
    expect(screen.getByText("1 still held")).toBeVisible();
    expect(screen.getByText(/median 2m.*p95 10m/)).toBeVisible();
    expect(screen.getByText(/median 1m.*p95 5m/)).toBeVisible();
  });
});
