import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, type LandingEntry } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import { LandingBadge } from ".";
import { LandingQueueProvider } from "./landing_queue";

const entry = (over: Partial<LandingEntry>): LandingEntry => ({
  ticket_id: 7,
  attempt_number: 1,
  change_url: "",
  state: "landing",
  position: 1,
  step: "waiting_checks",
  back_reason: "",
  batch_id: null,
  batch_position: 0,
  since_update_seconds: 0,
  ...over,
});

describe("LandingBadge", () => {
  it("shows the step of the ticket's own landing only", async () => {
    vi.spyOn(apiClient, "listLandingQueue").mockResolvedValue([entry({}), entry({ ticket_id: 8, step: "merging" })]);
    await renderSignedIn(
      <LandingQueueProvider refreshToken={0}>
        <LandingBadge ticketId={7} className="b" />
      </LandingQueueProvider>,
    );
    expect(await screen.findByText("landing · waiting for CI")).toBeInTheDocument();
    expect(screen.queryByText("landing · merging")).not.toBeInTheDocument();
  });

  it("shows the time in step, and marks a stuck merge step stalled", async () => {
    vi.spyOn(apiClient, "listLandingQueue").mockResolvedValue([entry({ step: "merging", since_update_seconds: 1200 })]);
    const { container } = await renderSignedIn(
      <LandingQueueProvider refreshToken={0}>
        <LandingBadge ticketId={7} className="b" />
      </LandingQueueProvider>,
    );
    expect(await screen.findByText("· 20m")).toBeInTheDocument();
    expect(container.querySelector(".b--landing-stalled")).not.toBeNull();
  });

  it("shows nothing for a ticket not in the queue", async () => {
    const list = vi.spyOn(apiClient, "listLandingQueue").mockResolvedValue([]);
    const { container } = await renderSignedIn(
      <LandingQueueProvider refreshToken={0}>
        <LandingBadge ticketId={7} className="b" />
      </LandingQueueProvider>,
    );
    await vi.waitFor(() => expect(list).toHaveBeenCalled());
    expect(container.querySelector(".b")).toBeNull();
  });

  it("shows nothing on a card of another attempt than the one landing", async () => {
    const list = vi.spyOn(apiClient, "listLandingQueue").mockResolvedValue([entry({ attempt_number: 2 })]);
    const { container } = await renderSignedIn(
      <LandingQueueProvider refreshToken={0}>
        <LandingBadge ticketId={7} attemptNumber={1} className="b" />
        <LandingBadge ticketId={7} attemptNumber={2} className="c" />
      </LandingQueueProvider>,
    );
    await screen.findByText("landing · waiting for CI");
    expect(list).toHaveBeenCalled();
    expect(container.querySelector(".b")).toBeNull();
    expect(container.querySelector(".c")).not.toBeNull();
  });

  it("refetches when the board refreshes, keeping the badge meanwhile", async () => {
    const list = vi.spyOn(apiClient, "listLandingQueue").mockResolvedValueOnce([entry({})]);
    function Board() {
      const [token, setToken] = useState(0);
      return (
        <LandingQueueProvider refreshToken={token}>
          <button onClick={() => setToken((n) => n + 1)}>refresh</button>
          <LandingBadge ticketId={7} className="b" />
        </LandingQueueProvider>
      );
    }
    await renderSignedIn(<Board />);
    await screen.findByText("landing · waiting for CI");

    list.mockReturnValueOnce(new Promise(() => {}));
    await userEvent.click(screen.getByRole("button", { name: "refresh" }));

    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(screen.getByText("landing · waiting for CI")).toBeInTheDocument();
  });
});
