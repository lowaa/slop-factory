import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { LandingQueueProvider } from "@/landing_state";
import { makeAttemptCard, makeProfile, makeTicket } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import TicketCard from "./ticket_card";

describe("TicketCard", () => {
  it("shows a needs-discussion marker when the ticket needs discussion", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ needs_discussion: true })} />);

    expect(screen.getByText("needs discussion")).toBeInTheDocument();
  });

  it("does not show the needs-discussion marker otherwise", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ needs_discussion: false })} />);

    expect(screen.queryByText("needs discussion")).not.toBeInTheDocument();
  });

  it("shows a requires-approval marker when the ticket requires human PR approval", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ requires_approval: true })} />);

    expect(screen.getByText("requires approval")).toBeInTheDocument();
  });

  it("does not show the requires-approval marker otherwise", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ requires_approval: false })} />);

    expect(screen.queryByText("requires approval")).not.toBeInTheDocument();
  });

  it("shows a waiting-on-you cue for a ticket in discussion", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "discussion" })} />);

    expect(screen.getByText("waiting on you")).toBeInTheDocument();
  });

  it("does not show the waiting-on-you cue for other statuses", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "open" })} />);

    expect(screen.queryByText("waiting on you")).not.toBeInTheDocument();
  });

  it("shows a waiting-on-your-review cue for a ticket in review", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "review" })} />);

    expect(screen.getByText("waiting on your review")).toBeInTheDocument();
  });

  it("does not show the waiting-on-your-review cue for other statuses", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "open" })} />);

    expect(screen.queryByText("waiting on your review")).not.toBeInTheDocument();
  });

  it("can show both markers at once", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "discussion", needs_discussion: true })} />);

    expect(screen.getByText("needs discussion")).toBeInTheDocument();
    expect(screen.getByText("waiting on you")).toBeInTheDocument();
  });

  it("shows a not-in-queue cue for a preparing ticket", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "preparing" })} />);

    expect(screen.getByText("not in queue")).toBeInTheDocument();
  });

  it("marks a parked ticket as not planned, and only a parked one", async () => {
    const { unmount } = await renderSignedIn(<TicketCard ticket={makeTicket({ status: "parked" })} />);
    expect(screen.getByText("parked -- not planned")).toBeInTheDocument();
    unmount();

    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "preparing" })} />);
    expect(screen.queryByText("parked -- not planned")).not.toBeInTheDocument();
  });

  it("does not show the not-in-queue cue for other statuses", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "open" })} />);

    expect(screen.queryByText("not in queue")).not.toBeInTheDocument();
  });

  it("shows a single required role without a match word", async () => {
    await renderSignedIn(
      <TicketCard ticket={makeTicket({ required_roles: [{ id: "r1", name: "deploy" }], role_match: "any" })} />,
    );

    expect(screen.getByText("role: deploy")).toBeInTheDocument();
  });

  it("shows several required roles with their match mode", async () => {
    await renderSignedIn(
      <TicketCard
        ticket={makeTicket({
          required_roles: [
            { id: "r1", name: "deploy" },
            { id: "r2", name: "review" },
          ],
          role_match: "all",
        })}
      />,
    );

    expect(screen.getByText("roles: all of deploy, review")).toBeInTheDocument();
  });

  it("shows no role badge for a ticket that requires none", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ required_roles: [] })} />);

    expect(screen.queryByText(/^role/)).not.toBeInTheDocument();
  });

  it("shows a compact warning when no enabled agent can claim the ticket", async () => {
    await renderSignedIn(
      <TicketCard
        ticket={makeTicket({ required_roles: [{ id: "r1", name: "deploy" }], no_eligible_agent: true })}
      />,
    );

    expect(screen.getByText("no eligible agent")).toBeInTheDocument();
  });

  it("shows no eligibility warning when an agent can claim it", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ no_eligible_agent: false })} />);

    expect(screen.queryByText("no eligible agent")).not.toBeInTheDocument();
  });

  it("is a link to the ticket that names the tab and the session's tenant", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ id: 3, title: "Discuss this" })} />, {
      profile: makeProfile({ tenant_id: "tenant-a" }),
    });

    expect(screen.getByRole("link", { name: /Discuss this/ })).toHaveAttribute(
      "href",
      "/tickets/3?from=board&tenant=tenant-a",
    );
  });

  it("opens the ticket when a card carrying markers is clicked", async () => {
    const user = userEvent.setup();
    const { router } = await renderSignedIn(
      <TicketCard ticket={makeTicket({ id: 3, title: "Discuss this", status: "discussion" })} />,
      { profile: makeProfile({ tenant_id: "tenant-a" }) },
    );

    await user.click(screen.getByText("Discuss this"));

    expect(router.state.location.href).toBe("/tickets/3?from=board&tenant=tenant-a");
  });

  it("links to the ticket's PR, outside the ticket's own link", async () => {
    const user = userEvent.setup();
    const pr = "https://example.test/org/repo/pull/7";
    const { router } = await renderSignedIn(<TicketCard ticket={makeTicket({ id: 3, status: "done", pr_url: pr })} />, {
      profile: makeProfile({ tenant_id: "tenant-a" }),
    });
    const before = router.state.location.href;

    const link = screen.getByRole("link", { name: "PR for #3" });
    expect(link).toHaveAttribute("href", pr);
    expect(link.parentElement?.closest("a")).toBeNull();

    // Following it must not also open the ticket.
    link.addEventListener("click", (e) => e.preventDefault());
    await user.click(link);
    expect(router.state.location.href).toBe(before);
  });

  it("shows no PR link for a ticket without one", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ status: "done" })} />);

    expect(screen.queryByRole("link", { name: /PR/ })).not.toBeInTheDocument();
  });
  describe("release cooldown", () => {
    const NOW = new Date("2026-10-10T12:00:00Z");
    const LATER = new Date("2026-10-10T12:30:00Z");

    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(NOW);
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it("shows when a blocked ticket is next re-checked, with the full time on hover", async () => {
      await renderSignedIn(<TicketCard ticket={makeTicket({ status: "blocked", blocked_until: LATER.toISOString() })} />);

      const time = screen.getByText(/re-check after/);
      expect(time).toHaveAttribute("datetime", LATER.toISOString());
      expect(time).toHaveAttribute("title", LATER.toLocaleString());
      expect(time).toHaveTextContent(LATER.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }));
    });

    it.each([
      ["blocked only by dependencies", { status: "blocked" as const, blocked_until: null }],
      ["past its cooldown", { status: "blocked" as const, blocked_until: new Date(NOW.getTime() - 1000).toISOString() }],
      ["no longer blocked", { status: "open" as const, blocked_until: LATER.toISOString() }],
    ])("shows no re-check time for a ticket %s", async (_name, fields) => {
      await renderSignedIn(<TicketCard ticket={makeTicket(fields)} />);

      expect(screen.queryByText(/re-check after/)).not.toBeInTheDocument();
    });
  });

});

describe("TicketCard of an externally filed ticket", () => {
  const external = { source_id: "55555555-5555-5555-5555-555555555555", key: "zd-1", title: "Printer on fire" };

  it("shows what the sender called it, marked as external, with the board's own title on hover", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket({ title: "[desk] external report", external })} />);

    expect(screen.getByText("Printer on fire")).toHaveAttribute("title", "[desk] external report");
    expect(screen.getByText("external")).toBeInTheDocument();
  });

  it("falls back to the board's title when the sender gave none", async () => {
    await renderSignedIn(
      <TicketCard ticket={makeTicket({ title: "[desk] external report", external: { ...external, title: "" } })} />,
    );

    expect(screen.getByText("[desk] external report")).toBeInTheDocument();
  });

  it("renders a hostile title as text", async () => {
    const { container } = await renderSignedIn(
      <TicketCard ticket={makeTicket({ external: { ...external, title: "<img src=x onerror=alert(1)>" } })} />,
    );

    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
  });

  it("has no external marker on an ordinary ticket", async () => {
    await renderSignedIn(<TicketCard ticket={makeTicket()} />);

    expect(screen.queryByText("external")).not.toBeInTheDocument();
  });

  describe("an attempt card", () => {
    it("shows the attempt number, its place among the attempts, the variant and the worker cost", async () => {
      const card = makeAttemptCard(
        { id: 42, title: "Two ways" },
        {
          number: 2,
          total: 3,
          variant: { name: "terse", version: 4, version_id: "v", archived: false },
          worker_cost: [
            { billing_mode: "metered", cost_micros: 1_500_000, input_tokens: 1, output_tokens: 1, requests: 1, unpriced_records: 0 },
          ],
        },
      );
      await renderSignedIn(<TicketCard ticket={card} />);

      expect(screen.getByText(/#42/)).toHaveTextContent("#42 · 2");
      expect(screen.getByText("Two ways")).toBeInTheDocument();
      expect(screen.getByText("2 of 3 attempts")).toBeInTheDocument();
      expect(screen.getByText("terse v4")).toBeInTheDocument();
      expect(screen.getByText("$1.50 spent")).toBeInTheDocument();
      expect(screen.queryByText("awaiting choice")).not.toBeInTheDocument();
    });

    it("says a finished, unpromoted attempt in Review awaits a choice", async () => {
      await renderSignedIn(<TicketCard ticket={makeAttemptCard({ status: "review" }, { awaiting_choice: true })} />);

      expect(screen.getByText("awaiting choice")).toBeInTheDocument();
      expect(screen.getByText("waiting on your review")).toBeInTheDocument();
    });

    it("reads usage nothing could price as unpriced, never $0.00", async () => {
      const card = makeAttemptCard(
        {},
        { worker_cost: [{ billing_mode: "metered", cost_micros: null, input_tokens: 1, output_tokens: 1, requests: 1, unpriced_records: 1 }] },
      );
      await renderSignedIn(<TicketCard ticket={card} />);

      expect(screen.getByText("unpriced")).toBeInTheDocument();
    });

    it("labels an estimated figure, and notes unpriced records beside a priced one", async () => {
      const card = makeAttemptCard(
        {},
        {
          worker_cost: [
            { billing_mode: "subscription", cost_micros: 2_000_000, input_tokens: 1, output_tokens: 1, requests: 1, unpriced_records: 1 },
            { billing_mode: "metered", cost_micros: 500_000, input_tokens: 1, output_tokens: 1, requests: 1, unpriced_records: 0 },
          ],
        },
      );
      await renderSignedIn(<TicketCard ticket={card} />);

      expect(screen.getByText("$2.00 estimated + unpriced")).toBeInTheDocument();
      expect(screen.getByText("$0.50 spent")).toBeInTheDocument();
    });

    it("opens the attempt's tab, and its PR link names the attempt", async () => {
      const card = makeAttemptCard({ id: 42, pr_url: "https://example.test/pull/9" }, { number: 2 });
      await renderSignedIn(<TicketCard ticket={card} />);

      expect(screen.getByRole("link", { name: /^#42/ })).toHaveAttribute(
        "href",
        expect.stringMatching(/\/tickets\/42\/attempts\/2\?.*from=board/),
      );
      expect(screen.getByRole("link", { name: "PR for #42 attempt 2" })).toBeInTheDocument();
    });

    it("leaves a one-attempt ticket's card as it was: no attempt number, count or variant", async () => {
      await renderSignedIn(<TicketCard ticket={makeTicket({ id: 42, status: "in_progress", pr_url: "https://example.test/pull/9" })} />);

      expect(screen.getByText(/#42/)).toHaveTextContent(/^#42$/);
      expect(screen.getByRole("link", { name: /^#42/ })).not.toHaveAttribute("href", expect.stringContaining("/attempts/"));
      expect(screen.queryByText(/attempts/)).not.toBeInTheDocument();
      expect(screen.queryByText("awaiting choice")).not.toBeInTheDocument();
      expect(screen.getByRole("link", { name: "PR for #42" })).toBeInTheDocument();
    });
  });
});

describe("TicketCard landing badge", () => {
  it("shows where the server has the ticket's change in its landing", async () => {
    vi.spyOn(apiClient, "listLandingQueue").mockResolvedValue([
      { ticket_id: 7, attempt_number: 1, change_url: "", state: "back_with_agent", position: 0, step: "", back_reason: "ci_red", batch_id: null, batch_position: 0, since_update_seconds: 0 },
    ]);
    await renderSignedIn(
      <LandingQueueProvider refreshToken={0}>
        <TicketCard ticket={makeTicket({ id: 7 })} />
      </LandingQueueProvider>,
    );

    expect(await screen.findByText("back with agent")).toBeInTheDocument();
  });
});
