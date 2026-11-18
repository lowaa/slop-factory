import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile, makeTicket } from "@/test/fixtures";
import { forgetBoardColumn } from "@/domain/board_columns";
import { stubNarrowLayout } from "@/test/layout";
import { serveTickets } from "@/test/serve_tickets";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { clickAccountLink } from "@/test/header";
import { FIRST_PAINT_TIMEOUT_MS } from "@/test/timeouts";

// The phone layout's markup: the header's menu, and the board's column
// in the address. How it looks is e2e's layout.spec.ts.
describe("on a phone", () => {
  function signIn() {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace", tenant_id: "tenant-a" }));
    mockBoardShell();
  }

  function chip(name: string): HTMLElement {
    return screen.getByRole("button", { name: new RegExp(`^${name} \\d+$`) });
  }

  it("keeps the tabs and account in the header's menu", async () => {
    window.history.pushState({}, "", "/");
    stubNarrowLayout();
    signIn();
    const user = userEvent.setup();

    renderApp();
    const menu = await screen.findByRole("button", { name: "Menu" }, { timeout: FIRST_PAINT_TIMEOUT_MS });
    // The processing switch stays out of the menu, on show.
    expect(await screen.findByRole("switch", { name: "Ticket processing" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Workers" })).not.toBeInTheDocument();
    expect(screen.getByText("Ada Lovelace")).not.toBeVisible();

    await user.click(menu);
    expect(screen.getByText("Ada Lovelace")).toBeVisible();
    await user.click(screen.getByRole("link", { name: "Workers" }));

    expect(window.location.pathname).toBe("/workers");
    expect(menu).toHaveAttribute("aria-expanded", "false");
  });

  it("has no menu on a wide window", async () => {
    window.history.pushState({}, "", "/");
    signIn();

    renderApp();

    expect(
      await screen.findByRole("link", { name: "Workers" }, { timeout: FIRST_PAINT_TIMEOUT_MS }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Menu" })).not.toBeInTheDocument();
  });

  describe("the board's column", () => {
    afterEach(() => {
      forgetBoardColumn();
    });

    it("opens on the column its address names", async () => {
      window.history.pushState({}, "", "/?column=blocked");
      signIn();

      renderApp();

      await waitFor(() => expect(chip("Blocked")).toHaveAttribute("aria-pressed", "true"), {
        timeout: FIRST_PAINT_TIMEOUT_MS,
      });
    });

    it("puts the chosen column in the address, in place of the entry it was on", async () => {
      window.history.pushState({}, "", "/");
      signIn();
      const user = userEvent.setup();

      renderApp();
      await screen.findByRole("group", { name: "Board column" }, { timeout: FIRST_PAINT_TIMEOUT_MS });
      const entries = window.history.length;
      await user.click(chip("Review"));

      expect(window.location.pathname + window.location.search).toBe("/?column=review");
      expect(chip("Review")).toHaveAttribute("aria-pressed", "true");
      expect(window.history.length).toBe(entries);
    });

    // The ticket's own Back link is a plain link to "/", with no column.
    it("is still the chosen one after a ticket's Back link", async () => {
      window.history.pushState({}, "", "/");
      signIn();
      // Something in progress, so the default column is not the chosen one.
      serveTickets(() => [
        makeTicket({ id: 7, title: "Linkable", status: "blocked" }),
        makeTicket({ id: 8, title: "Under way", status: "in_progress" }),
      ]);
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Linkable", status: "blocked" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
      vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
      vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const user = userEvent.setup();

      renderApp();
      await screen.findByText("Linkable", {}, { timeout: FIRST_PAINT_TIMEOUT_MS });
      await user.click(chip("Blocked"));
      await user.click(screen.getByText("Linkable"));
      await user.click(
        await screen.findByRole("link", { name: "← Back to board" }, { timeout: FIRST_PAINT_TIMEOUT_MS }),
      );

      await screen.findByText("Linkable");
      expect(window.location.pathname + window.location.search).toBe("/");
      expect(chip("Blocked")).toHaveAttribute("aria-pressed", "true");
    });

    it("is still the one a deep link named after a ticket's Back link", async () => {
      window.history.pushState({}, "", "/?column=blocked");
      signIn();
      serveTickets(() => [
        makeTicket({ id: 7, title: "Linkable", status: "blocked" }),
        makeTicket({ id: 8, title: "Under way", status: "in_progress" }),
      ]);
      vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Linkable", status: "blocked" }));
      vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
      vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
      vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
      vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
      const user = userEvent.setup();

      renderApp();
      await user.click(await screen.findByText("Linkable", {}, { timeout: FIRST_PAINT_TIMEOUT_MS }));
      await user.click(
        await screen.findByRole("link", { name: "← Back to board" }, { timeout: FIRST_PAINT_TIMEOUT_MS }),
      );

      await screen.findByText("Linkable");
      expect(chip("Blocked")).toHaveAttribute("aria-pressed", "true");
    });

    // On a shared browser the next person starts from the default.
    it("is forgotten on sign-out", async () => {
      window.history.pushState({}, "", "/?column=blocked");
      signIn();
      serveTickets(() => [makeTicket({ id: 8, status: "in_progress" })]);
      vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
      const user = userEvent.setup();

      renderApp();
      await waitFor(() => expect(chip("Blocked")).toHaveAttribute("aria-pressed", "true"), {
        timeout: FIRST_PAINT_TIMEOUT_MS,
      });
      await clickAccountLink(user, "Sign out");
      await screen.findByRole("heading", { name: "Sign in" }, { timeout: FIRST_PAINT_TIMEOUT_MS });

      window.history.pushState({}, "", "/");
      cleanup();
      renderApp();
      await waitFor(() => expect(chip("In progress")).toHaveAttribute("aria-pressed", "true"), {
        timeout: FIRST_PAINT_TIMEOUT_MS,
      });
    });
  });
});
