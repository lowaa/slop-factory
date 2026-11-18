// Test-only module, never hot-reloaded, so fast refresh is no concern.
/* oxlint-disable react/only-export-components */
import { mockApi } from "@/test/mock_api";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { selectedValueOf } from "@/test/widgets";
import { makeTicket } from "@/test/fixtures";
import TicketDetailView from "./ticket_detail";
import type { TicketView } from "./ticket_view";
import type { AttemptLink } from "./attempt_view";

// TicketDetail with its tab kept in state, as the route keeps it in the
// URL -- so a test can switch tabs without standing up a router.
//
// The attempt on show is kept the same way, and its tabs are plain links that
// set it.
export function TicketDetail({
  initialView = "main",
  initialAttempt,
  ...props
}: {
  ticketId: number;
  back?: ReactNode;
  initialView?: TicketView;
  initialAttempt?: number;
}) {
  const [view, setView] = useState<TicketView>(initialView);
  const [attempt, setAttempt] = useState<number | undefined>(initialAttempt);
  const attemptLink: AttemptLink = (target, { children, ...rest }) => (
    <a
      href={target === null ? "#ticket" : `#attempt-${target}`}
      onClick={(e) => {
        e.preventDefault();
        setAttempt(target ?? undefined);
      }}
      {...rest}
    >
      {children}
    </a>
  );
  return <TicketDetailView {...props} view={view} onViewChange={setView} attempt={attempt} attemptLink={attemptLink} />;
}

export const CURRENT_USER_ID = "33333333-3333-3333-3333-333333333333";
export const EDIT = "Edit title and description";

// Registers the shared beforeEach; call it inside the file's top describe.
export function stubTicketDetailDefaults() {
  // No ticket in these tests cares about handoffs or edit history unless
  // it says so -- this is the "there are none" default every other test
  // gets for free.
  beforeEach(() => {
    mockApi({
      listAgentContexts: [],
      listTicketAttachments: [],
      listRevisions: [],
      listAttemptReviewRuns: [],
      listStatusHistory: [],
      listVariantOptions: [],
      listAgentRoles: [],
      getTicketLlmUsage: { worker: [], guidance: [] },
    });
    // A member is refused the owner's merge facts; tests of the owner's
    // notice replace this.
    vi.spyOn(apiClient, "getTicketMergeFacts").mockRejectedValue(new ApiError(403, "only the workspace owner can do that"));
  });
}

export interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
}
export function deferred<T>(): Deferred<T> {
  const d = {} as Deferred<T>;
  d.promise = new Promise<T>((resolve, reject) => {
    d.resolve = resolve;
    d.reject = reject;
  });
  return d;
}

// Every ticket id gets the same ticket, so a switch to #6 lands on the
// same screen with the same action on offer: whatever is gone afterwards
// was cleared, not just no longer rendered.
export function load(base: Parameters<typeof makeTicket>[0]) {
  vi.spyOn(apiClient, "getTicket").mockImplementation(async (id: number) => makeTicket({ ...base, id }));
  vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
  vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
}

export async function switchTo(rerender: (ui: ReactNode) => void, id: number) {
  rerender(<TicketDetail ticketId={id} />);
  await screen.findByRole("heading", { name: new RegExp(`#${id}\\b`) });
}

export const noAlert = () => expect(screen.queryByRole("alert")).not.toBeInTheDocument();

// The buttons that are one request and a status move.
const statusButtons = [
  {
    name: "Open",
    label: "Open",
    busy: "Opening…",
    method: "markTicketReady",
    base: { status: "preparing", can_ready: true },
    landed: { status: "open" },
    shows: "open",
  },
  {
    name: "Park",
    label: "Park",
    busy: "Parking…",
    method: "updateTicket",
    base: { status: "open" },
    landed: { status: "parked" },
    shows: "parked",
  },
  {
    name: "Unpark",
    label: "Unpark",
    busy: "Unparking…",
    method: "updateTicket",
    base: { status: "parked" },
    landed: { status: "preparing" },
    shows: "preparing",
  },
  {
    name: "Approve",
    label: "Approve",
    busy: "Approving…",
    method: "approveTicket",
    base: { status: "review" },
    landed: { status: "open" },
    shows: "open",
  },
] as const;

function spyOnMove(method: (typeof statusButtons)[number]["method"]) {
  const api = apiClient as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>;
  return vi.spyOn(api, method);
}

type StatusButton = (typeof statusButtons)[number];

export function statusButtonRows(...names: StatusButton["name"][]) {
  return statusButtons.filter((c) => names.includes(c.name));
}

// The table-driven body shared by every file that owns a status button.
export function describeStatusButtons(rows: readonly StatusButton[]) {
  describe.each(rows)("$name", (c) => {
    it("is disabled and shows its busy label while in flight, then goes back to normal", async () => {
      const user = userEvent.setup();
      load(c.base);
      const reply = deferred<unknown>();
      spyOnMove(c.method).mockReturnValue(reply.promise);
      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: c.label }));

      expect(screen.getByRole("button", { name: c.busy })).toBeDisabled();
      expect(screen.queryByRole("button", { name: c.label })).not.toBeInTheDocument();

      // A reply that leaves the ticket where it was keeps the button on screen.
      await act(async () => reply.resolve(makeTicket({ id: 5, ...c.base })));

      expect(screen.getByRole("button", { name: c.label })).toBeEnabled();
      expect(screen.queryByRole("button", { name: c.busy })).not.toBeInTheDocument();
    });

    it("shows the server's message, clears it for a retry, and shows the ticket the retry lands on", async () => {
      const user = userEvent.setup();
      load(c.base);
      const retry = deferred<unknown>();
      spyOnMove(c.method)
        .mockRejectedValueOnce(new ApiError(409, "ticket: refused this time"))
        .mockReturnValueOnce(retry.promise);
      render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: c.label }));
      expect(await screen.findByRole("alert")).toHaveTextContent("ticket: refused this time");
      expect(screen.getByRole("button", { name: c.label })).toBeEnabled();

      await user.click(screen.getByRole("button", { name: c.label }));
      noAlert();

      await act(async () => retry.resolve(makeTicket({ id: 5, ...c.landed })));
      noAlert();
      await waitFor(() => expect(selectedValueOf("Status")).toContain(c.shows));
    });

    it("drops its error when the selected ticket changes", async () => {
      const user = userEvent.setup();
      load(c.base);
      spyOnMove(c.method).mockRejectedValue(new ApiError(409, "ticket: refused this time"));
      const { rerender } = render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: c.label }));
      await screen.findByRole("alert");

      await switchTo(rerender, 6);

      expect(screen.getByRole("button", { name: c.label })).toBeEnabled();
      noAlert();
    });

    it("leaves the next ticket alone while, and after, the previous one's move is in flight", async () => {
      const user = userEvent.setup();
      load(c.base);
      const reply = deferred<unknown>();
      spyOnMove(c.method).mockReturnValue(reply.promise);
      const { rerender } = render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: c.label }));
      await switchTo(rerender, 6);

      expect(screen.getByRole("button", { name: c.label })).toBeEnabled();

      // Refused, so a failure that still counted as current would show.
      await act(async () => reply.reject(new ApiError(409, "ticket: refused this time")));

      noAlert();
      expect(screen.getByRole("button", { name: c.label })).toBeEnabled();
    });

    it("does not merge the previous ticket's late move into the next one", async () => {
      const user = userEvent.setup();
      load(c.base);
      const reply = deferred<unknown>();
      spyOnMove(c.method).mockReturnValue(reply.promise);
      const { rerender } = render(<TicketDetail ticketId={5} />);

      await user.click(await screen.findByRole("button", { name: c.label }));
      await switchTo(rerender, 6);
      const before = selectedValueOf("Status");

      await act(async () => reply.resolve(makeTicket({ id: 5, ...c.landed })));

      expect(selectedValueOf("Status")).toBe(before);
      expect(selectedValueOf("Status")).not.toContain(c.shows);
    });
  });
}
