import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, ApiError } from "@/api";
import { makeShadowDecisionsReport } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import ShadowDecisionsSection from "./shadow_decisions_section";

afterEach(() => {
  vi.restoreAllMocks();
});

const render = (days: 7 | 30 | 90 = 30) => renderSignedIn(<ShadowDecisionsSection days={days} />);

describe("ShadowDecisionsSection", () => {
  it("asks for the period as given", async () => {
    const spy = vi
      .spyOn(apiClient, "getShadowDecisionsReport")
      .mockResolvedValue(makeShadowDecisionsReport());

    await render();

    expect(await screen.findByTestId("shadow-decisions-none")).toBeVisible();
    expect(spy).toHaveBeenCalledWith({ days: 30 });
    expect(screen.queryByText("Would accept")).toBeNull();
  });

  it("asks for 30 days and says so when the period is 90", async () => {
    const spy = vi
      .spyOn(apiClient, "getShadowDecisionsReport")
      .mockResolvedValue(makeShadowDecisionsReport());

    await render(90);

    expect(await screen.findByTestId("shadow-decisions-capped")).toBeVisible();
    expect(spy).toHaveBeenCalledWith({ days: 30 });
  });

  it("shows the counts, the reasons, and the accepted changes whose checks failed, with links", async () => {
    vi.spyOn(apiClient, "getShadowDecisionsReport").mockResolvedValue(
      makeShadowDecisionsReport({
        would_accept: 5,
        would_refuse: 3,
        accept_ci_passed: 2,
        accept_ci_failed: 1,
        accept_awaiting_ci: 2,
        reasons: [
          {
            decision: "would_refuse",
            reason: "go.work is not in an independent app",
            count: 3,
          },
        ],
        failures: [
          {
            ticket_id: 701,
            title: "Break billing",
            change_id: 4,
            head_sha: "h1",
            ci_head_sha: "h2",
            drift_dirs: ["billing/", "slop-haus/"],
            decided_at: "2026-10-01T00:00:00Z",
            ci_recorded_at: "2026-10-01T01:00:00Z",
          },
        ],
      }),
    );

    await render();

    expect(await screen.findByText("Accepted, awaiting checks")).toBeVisible();
    expect(
      screen.getByText("Accepted, awaiting checks").nextElementSibling,
    ).toHaveTextContent("2");
    expect(
      screen.getByText("Accepted, checks failed").nextElementSibling,
    ).toHaveTextContent("1");
    expect(screen.getByTestId("shadow-decisions-reasons")).toHaveTextContent(
      "refuse: go.work is not in an independent app",
    );
    expect(
      screen.getByRole("link", { name: /#701 Break billing/ }),
    ).toBeVisible();
    expect(screen.getByText("billing/, slop-haus/")).toBeVisible();
  });

  it("says none failed rather than showing an empty list", async () => {
    vi.spyOn(apiClient, "getShadowDecisionsReport").mockResolvedValue(
      makeShadowDecisionsReport({ would_accept: 1, accept_ci_passed: 1 }),
    );

    await render();

    expect(
      await screen.findByTestId("shadow-decisions-failures"),
    ).toHaveTextContent("None.");
  });

  it("shows a timeout as its own message", async () => {
    vi.spyOn(apiClient, "getShadowDecisionsReport").mockRejectedValue(
      new ApiError(503, "slow"),
    );

    await render();

    expect(await screen.findByRole("alert")).toHaveTextContent("took too long");
  });
});
