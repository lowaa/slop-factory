import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type NamedIntegrationLock } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import IntegrationLockPanel from "./integration_lock_panel";

function makeLock(
  overrides: Partial<NamedIntegrationLock> = {},
): NamedIntegrationLock {
  return {
    name: "integration",
    holder: {
      agent_id: "11111111-1111-4111-8111-111111111111",
      agent_name: "merger",
      ticket_id: 7,
      ticket_title: "Ship the queue",
      pr_url: "https://github.com/o/r/pull/9",
      acquired_at: "2026-10-03T15:00:00Z",
      held_seconds: 20 * 60,
      last_heartbeat_at: "2026-10-03T15:19:00Z",
      since_heartbeat_seconds: 60,
      dead: false,
      long_held: false,
    },
    queue: [
      {
        agent_id: "22222222-2222-4222-8222-222222222222",
        agent_name: "first-in-line",
        ticket_id: 8,
        ticket_title: "Next up",
        queued_at: "2026-10-03T15:05:00Z",
        waited_seconds: 15 * 60,
        priority: "normal",
      },
      {
        agent_id: "33333333-3333-4333-8333-333333333333",
        agent_name: "second-in-line",
        ticket_id: 9,
        ticket_title: "After that",
        queued_at: "2026-10-03T15:10:00Z",
        waited_seconds: 3725,
        priority: "urgent",
        moved_up_by: "priority",
      },
    ],
    ...overrides,
  };
}

describe("IntegrationLockPanel", () => {
  it("shows each waiter's priority and why one was moved up", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const panel = await screen.findByRole("region", {
      name: "Integration lock",
    });
    expect(within(panel).getByText("normal priority")).toBeInTheDocument();
    expect(within(panel).getByText("urgent priority")).toBeInTheDocument();
    expect(within(panel).getByText("moved up by priority")).toBeInTheDocument();
  });

  it("shows the holder with its ticket, PR and how long, and the queue with each wait", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const panel = await screen.findByRole("region", {
      name: "Integration lock",
    });
    expect(within(panel).getByText("merger")).toBeInTheDocument();
    expect(
      within(panel).getByRole("link", { name: /#7 Ship the queue/ }),
    ).toBeInTheDocument();
    expect(
      within(panel).getByRole("link", { name: "Pull request of ticket #7" }),
    ).toHaveAttribute("href", "https://github.com/o/r/pull/9");
    expect(within(panel).getByText(/\(20m\)/)).toBeInTheDocument();

    const queue = within(panel).getByRole("list", {
      name: "Queue for the integration lock",
    });
    const entries = within(queue).getAllByRole("listitem");
    expect(entries).toHaveLength(2);
    expect(entries[0]).toHaveTextContent("first-in-line");
    expect(entries[0]).toHaveTextContent("waiting 15m");
    expect(entries[1]).toHaveTextContent("second-in-line");
    expect(entries[1]).toHaveTextContent("waiting 1h 2m");
  });

  it("flags a dead holder with a banner, the silence and the queue, beside the release button", async () => {
    const lock = makeLock();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      {
        ...lock,
        holder: {
          ...lock.holder!,
          dead: true,
          since_heartbeat_seconds: 72 * 60,
        },
      },
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const panel = await screen.findByRole("region", {
      name: "Integration lock",
    });
    expect(
      within(panel).getByText(
        /Dead holder: no heartbeat for 1h 12m, 2 queued behind it/,
      ),
    ).toBeInTheDocument();
    expect(
      within(panel).getByRole("button", { name: "Release lock" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(
      /merger.*no heartbeat for 1h 12m/,
    );
  });

  it("shows no dead-holder flag or banner for a live holder", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    await screen.findByRole("region", { name: "Integration lock" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText(/Dead holder/)).not.toBeInTheDocument();
  });

  it("warns when a live holder has held the lock past the threshold, linking its ticket and PR", async () => {
    const lock = makeLock();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      {
        ...lock,
        holder: { ...lock.holder!, long_held: true, held_seconds: 243 * 60 },
      },
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      /held for 4h 3m by merger on #7.*2 waiting.*checks may be stuck/,
    );
    expect(within(alert).getByRole("link", { name: "#7" })).toBeInTheDocument();
    expect(
      within(alert).getByRole("link", { name: /Pull request of ticket #7/ }),
    ).toHaveAttribute("href", "https://github.com/o/r/pull/9");
  });

  it("shows no long-hold warning under the threshold", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    await screen.findByRole("region", { name: "Integration lock" });
    expect(screen.queryByText(/checks may be stuck/)).not.toBeInTheDocument();
  });

  it("shows only the dead-holder warning for a holder both dead and long held", async () => {
    const lock = makeLock();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      {
        ...lock,
        holder: {
          ...lock.holder!,
          dead: true,
          long_held: true,
          held_seconds: 5 * 3600,
          since_heartbeat_seconds: 3600,
        },
      },
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const alerts = await screen.findAllByRole("alert");
    expect(alerts).toHaveLength(1);
    expect(alerts[0]).toHaveTextContent(/no heartbeat/);
    expect(screen.queryByText(/checks may be stuck/)).not.toBeInTheDocument();
  });

  it("shows nothing for an idle lock", async () => {
    const get = vi
      .spyOn(apiClient, "listIntegrationLocks")
      .mockResolvedValue([]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    await waitFor(() => expect(get).toHaveBeenCalled());

    expect(
      screen.queryByRole("region", { name: "Integration lock" }),
    ).not.toBeInTheDocument();
  });

  it("says a free lock is reserved for the head of the queue", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      makeLock({ holder: undefined }),
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    expect(
      await screen.findByText(/Free, reserved for the first in the queue/),
    ).toBeInTheDocument();
  });

  it("releases the lock only after a second, confirming click", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks")
      .mockResolvedValueOnce([makeLock()])
      .mockResolvedValue([]);
    const release = vi
      .spyOn(apiClient, "forceUnlockIntegration")
      .mockResolvedValue();

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    await user.click(
      await screen.findByRole("button", { name: "Release lock" }),
    );
    expect(release).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: "Release merger's lock" }),
    );

    expect(release).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "Integration lock" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("can back out of a release", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);
    const release = vi
      .spyOn(apiClient, "forceUnlockIntegration")
      .mockResolvedValue();

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    await user.click(
      await screen.findByRole("button", { name: "Release lock" }),
    );
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(release).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: "Release lock" }),
    ).toBeInTheDocument();
  });

  it("drops the queue entry that was chosen, by agent", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);
    const drop = vi
      .spyOn(apiClient, "dropIntegrationLockWaiter")
      .mockResolvedValue();

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    const second = (await screen.findAllByRole("listitem"))[1];
    await user.click(within(second).getByRole("button", { name: "Remove" }));
    await user.click(
      screen.getByRole("button", { name: "Remove second-in-line" }),
    );

    expect(drop).toHaveBeenCalledWith(
      "33333333-3333-4333-8333-333333333333",
      undefined,
    );
  });

  it("reports a refused release next to the lock instead of hiding it", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([makeLock()]);
    vi.spyOn(apiClient, "forceUnlockIntegration").mockRejectedValue(
      new ApiError(404, "lock not held"),
    );

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    await user.click(
      await screen.findByRole("button", { name: "Release lock" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Release merger's lock" }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("lock not held");
  });

  it("refetches when the board says something changed", async () => {
    const get = vi
      .spyOn(apiClient, "listIntegrationLocks")
      .mockResolvedValue([makeLock()]);

    const { rerender } = await renderSignedIn(
      <IntegrationLockPanel refreshToken={0} />,
    );
    await screen.findByRole("region", { name: "Integration lock" });
    expect(get).toHaveBeenCalledTimes(1);

    rerender(<IntegrationLockPanel refreshToken={1} />);
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  });
});

describe("IntegrationLockPanel with per-branch locks", () => {
  const branchLock = (): NamedIntegrationLock =>
    makeLock({
      name: "integration:app@release",
      repo: "app",
      branch: "release",
      holder: {
        ...makeLock().holder!,
        agent_name: "branch-merger",
        ticket_id: 70,
        ticket_title: "Release fix",
      },
    });

  it("renders a section per lock, each with its own buttons", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      makeLock(),
      branchLock(),
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    const def = await screen.findByRole("region", { name: "Integration lock" });
    const branch = screen.getByRole("region", {
      name: "Integration lock: app @ release",
    });
    expect(within(def).getByText("merger")).toBeInTheDocument();
    expect(within(branch).getByText("branch-merger")).toBeInTheDocument();
    expect(
      within(def).getAllByRole("button", { name: "Release lock" }),
    ).toHaveLength(1);
    expect(
      within(branch).getAllByRole("button", { name: "Release lock" }),
    ).toHaveLength(1);
    expect(
      within(branch).getAllByRole("button", { name: "Remove" }),
    ).toHaveLength(2);
  });

  it("releases the branch lock by name and not the default one", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      makeLock(),
      branchLock(),
    ]);
    const release = vi
      .spyOn(apiClient, "forceUnlockIntegration")
      .mockResolvedValue();

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    const branch = await screen.findByRole("region", {
      name: "Integration lock: app @ release",
    });
    await user.click(
      within(branch).getByRole("button", { name: "Release lock" }),
    );
    await user.click(
      within(branch).getByRole("button", {
        name: "Release branch-merger's lock",
      }),
    );

    expect(release).toHaveBeenCalledExactlyOnceWith("integration:app@release");
  });

  it("drops a waiter from the branch lock's queue by name", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      makeLock(),
      branchLock(),
    ]);
    const drop = vi
      .spyOn(apiClient, "dropIntegrationLockWaiter")
      .mockResolvedValue();

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);
    const queue = await screen.findByRole("list", {
      name: "Queue for the integration lock: app @ release",
    });
    await user.click(
      within(within(queue).getAllByRole("listitem")[0]).getByRole("button", {
        name: "Remove",
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Remove first-in-line" }),
    );

    expect(drop).toHaveBeenCalledExactlyOnceWith(
      "22222222-2222-4222-8222-222222222222",
      "integration:app@release",
    );
  });

  it("names the branch lock in its dead-holder banner", async () => {
    const lock = branchLock();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      makeLock(),
      {
        ...lock,
        holder: { ...lock.holder!, dead: true, since_heartbeat_seconds: 3600 },
      },
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /lock for app @ release is held by branch-merger/,
    );
  });
});

describe("IntegrationLockPanel durations", () => {
  it("shows a lock held for 30 hours in days", async () => {
    const lock = makeLock();
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      { ...lock, holder: { ...lock.holder!, held_seconds: 30 * 3600 } },
    ]);

    await renderSignedIn(<IntegrationLockPanel refreshToken={0} />);

    expect(await screen.findByText(/\(1d 6h\)/)).toBeInTheDocument();
  });
});
