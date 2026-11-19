import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, ApiError } from "@/api";
import { makeScopeCostReport } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import ScopeCostSection from "./scope_cost_section";

afterEach(() => {
  vi.restoreAllMocks();
});

const render = () => renderSignedIn(<ScopeCostSection days={7} />);

describe("ScopeCostSection", () => {
  it("asks for 30 days at most, and says so, when the page's period is 90", async () => {
    const spy = vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(makeScopeCostReport());

    await renderSignedIn(<ScopeCostSection days={90} />);

    expect(await screen.findByTestId("scope-cost-capped")).toBeVisible();
    expect(spy).toHaveBeenCalledWith({ days: 30 });
  });

  it("asks for the period as given when it is kept", async () => {
    const spy = vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(makeScopeCostReport());

    await render();

    expect(await screen.findByTestId("scope-cost-none")).toBeVisible();
    expect(screen.queryByTestId("scope-cost-capped")).toBeNull();
    expect(spy).toHaveBeenCalledWith({ days: 7 });
  });

  it("shows the waits, the idle capacity and who held tickets back, with links", async () => {
    vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(
      makeScopeCostReport({
        intervals: 2,
        waits: { tickets: 2, claimed: 1, ongoing: 1, total_seconds: 7200, median_seconds: 3600 },
        blocked_seconds: 5400,
        idle_blocked_seconds: 1800,
        tickets: [{ id: 692, title: "Console kind", seconds: 3600, ongoing: true, claimed: false, blocker_ids: [684] }],
        blockers: [{ id: 684, title: "Migration", seconds: 7200, tickets: 2 }],
        paths: [{ path: "slop-haus/server/internal/schema/migrations/", seconds: 7200, tickets: 2 }],
        narrowings: 1,
      }),
    );

    await render();

    expect(await screen.findByText("Idle worker while held back")).toBeVisible();
    expect(screen.getByText("30m")).toBeVisible();
    expect(screen.getByText("median 1h")).toBeVisible();
    expect(screen.getByRole("link", { name: /#692 Console kind/ })).toBeVisible();
    expect(screen.getByText("1h (ongoing)")).toBeVisible();
    expect(screen.getByRole("link", { name: /#684 Migration/ })).toBeVisible();
    expect(screen.getByText("slop-haus/server/internal/schema/migrations/")).toBeVisible();
    expect(screen.getByTestId("scope-cost-narrowings")).toHaveTextContent("narrowed their scope 1 time ");
  });

  it("says scope locking is off, so an empty report is not a clean one", async () => {
    vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(makeScopeCostReport({ locking_off: true }));

    await render();

    expect(await screen.findByTestId("scope-cost-locking-off")).toHaveTextContent("Scope locking is off");
  });

  it("says nothing was held back rather than showing zeros", async () => {
    vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(makeScopeCostReport());

    await render();

    expect(await screen.findByTestId("scope-cost-none")).toBeVisible();
    expect(screen.queryByText("Total wait")).toBeNull();
  });

  it("shows the refused extensions and how they ended", async () => {
    vi.spyOn(apiClient, "getScopeCostReport").mockResolvedValue(
      makeScopeCostReport({
        denials: { total: 3, granted: 1, narrowed: 1, gave_up: 0, open: 1, median_seconds_to_resolve: 90, paths: [{ path: "docs/", count: 3 }] },
      }),
    );

    await render();

    expect(await screen.findByTestId("scope-cost-denials")).toHaveTextContent(
      "3 refused: 1 later granted, 1 narrowed, 0 gave up, 1 still open. Median time to resolve 1m 30s.",
    );
  });

  it("explains a report that took too long", async () => {
    vi.spyOn(apiClient, "getScopeCostReport").mockRejectedValue(new ApiError(503, "slow"));

    await render();

    expect(await screen.findByRole("alert")).toHaveTextContent("took too long");
  });
});
