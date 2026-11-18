import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import type { Status } from "@/api";
import { AuthContext } from "@/context/auth";
import { makePath, makeTicket } from "@/test/fixtures";
import { signedInAuth } from "@/test/render_signed_in";
import Board from "./board";
import { statusLabel } from "@/domain/board_columns";

// The board's cards are router Links naming the session's tenant, so the
// stories need both a router and a signed-in auth context.
const meta = {
  title: "Pages/Board",
  component: Board,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <TestRouterProvider>
        <AuthContext.Provider value={signedInAuth()}>
          <Story />
        </AuthContext.Provider>
      </TestRouterProvider>
    ),
  ],
} satisfies Meta<typeof Board>;

export default meta;
type Story = StoryObj<typeof meta>;

const LIVE: Status[] = ["preparing", "open", "in_progress", "blocked", "discussion", "review"];
const WITH_DONE: Status[] = [...LIVE, "done"];
const WITH_PARKED: Status[] = ["parked", ...LIVE];

const tickets = [
  makeTicket({
    id: 1,
    status: "preparing",
    title: "Shape the intake form",
    paths: [makePath({ path: "server/intake/" })],
  }),
  makeTicket({
    id: 2,
    status: "open",
    priority: "high",
    title: "Add the review status",
  }),
  makeTicket({ id: 3, status: "open", title: "Tidy the board scss" }),
  makeTicket({ id: 4, status: "in_progress", title: "Split the ticket card" }),
  makeTicket({ id: 5, status: "blocked", title: "Wait on the schema change" }),
  makeTicket({
    id: 6,
    status: "discussion",
    needs_discussion: true,
    title: "Which provider to use",
  }),
  makeTicket({
    id: 7,
    status: "review",
    requires_approval: true,
    title: "Ship the approvals panel",
  }),
  makeTicket({ id: 8, status: "done", title: "Old finished work" }),
];

// Holds the column the way the page does, so a chip press moves the board.
function PhoneBoard({ initial }: { initial?: Status }) {
  const [column, setColumn] = useState<Status | undefined>(initial);
  return (
    <Board
      tickets={tickets}
      statuses={WITH_DONE}
      column={column}
      onColumnChange={setColumn}
    />
  );
}

export const Desktop: Story = {
  args: { tickets, statuses: WITH_DONE },
};

export const DesktopWithParked: Story = {
  args: {
    tickets: [
      ...tickets,
      makeTicket({ id: 9, status: "parked", title: "Someday" }),
    ],
    statuses: [...WITH_PARKED, "done"],
  },
};

function SelectBoard() {
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set([2]));
  return (
    <Board
      tickets={tickets}
      statuses={WITH_DONE}
      selection={{
        selected,
        onToggle: (id) =>
          setSelected((cur) => {
            const next = new Set(cur);
            if (!next.delete(id)) next.add(id);
            return next;
          }),
        onSelectAll: (ids) => setSelected((cur) => new Set([...cur, ...ids])),
        onClear: (ids) =>
          setSelected(
            (cur) => new Set([...cur].filter((id) => !ids.includes(id))),
          ),
      }}
    />
  );
}

export const SelectMode: Story = {
  args: { tickets, statuses: WITH_DONE },
  render: () => <SelectBoard />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // Open holds tickets 2 (preselected) and 3.
    const selectAll = await canvas.findByRole("button", {
      name: "Select all in Open",
    });
    const clear = canvas.getByRole("button", { name: "Clear Open" });
    await expect(selectAll).toBeEnabled();
    await expect(clear).toBeEnabled();
    await userEvent.click(selectAll);
    await expect(selectAll).toBeDisabled();
    await userEvent.click(clear);
    await expect(clear).toBeDisabled();
    await expect(selectAll).toBeEnabled();
  },
};

// Below the narrow breakpoint the chips pick the one column shown.
export const PhoneWidth: Story = {
  args: { tickets, statuses: WITH_DONE },
  globals: { viewport: { value: "mobile1", isRotated: false } },
  render: () => <PhoneBoard />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    const chips = within(
      await canvas.findByRole("group", { name: "Board column" }),
    );
    const titleOf = new Map(tickets.map((t) => [t.status, t.title]));
    for (const chip of chips.getAllByRole("button")) {
      await userEvent.click(chip);
      const status = tickets.find((t) =>
        chip.textContent?.startsWith(statusLabel(t.status)),
      )?.status;
      // Only the chosen column's cards are on screen.
      // The Done column is a virtual list, which draws no rows while its
      // column is hidden: its card exists only once the column is shown.
      for (const [other, title] of titleOf) {
        if (other === status) {
          await expect(await canvas.findByText(title)).toBeVisible();
        } else {
          const card = canvas.queryByText(title);
          if (card) await expect(card).not.toBeVisible();
        }
      }
      await expect(chip).toHaveAttribute("aria-pressed", "true");
      await expect(
        chips
          .getAllByRole("button")
          .filter((c) => c.getAttribute("aria-pressed") === "true"),
      ).toHaveLength(1);
    }
  },
};

const doneTickets = Array.from({ length: 3000 }, (_, i) =>
  makeTicket({ id: 10_000 - i, status: "done", title: `Finished ticket ${10_000 - i}` }),
);

// A Done column of thousands: only the cards on screen are drawn, and the
// rest arrive by "Load more", a page at a time.
function LongDoneBoard() {
  const [shown, setShown] = useState(1000);
  return (
    <Board
      tickets={[...tickets.filter((t) => t.status !== "done"), ...doneTickets.slice(0, shown)]}
      statuses={WITH_DONE}
      paging={{
        done: {
          loading: false,
          hasMore: shown < doneTickets.length,
          loadingMore: false,
          error: null,
          onLoadMore: () => setShown((n) => n + 1000),
          onRetry: () => {},
        },
      }}
    />
  );
}

export const LongDoneColumn: Story = {
  args: { tickets, statuses: WITH_DONE },
  render: () => <LongDoneBoard />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("link", { name: /Finished ticket 10000/ })).toBeVisible();
    // Far fewer than the 1,000 loaded are in the page.
    await expect(canvas.queryByRole("link", { name: /Finished ticket 9100\b/ })).toBeNull();
    // The count is in both the chip (phone) and the column header.
    await expect(canvas.getAllByText("1000+")).toHaveLength(2);
    await userEvent.click(canvas.getByRole("button", { name: "Load more" }));
    await expect(await canvas.findAllByText("2000+")).toHaveLength(2);
  },
};

export const ColumnFailedToLoad: Story = {
  args: {
    tickets: tickets.filter((t) => t.status !== "review"),
    statuses: LIVE,
    paging: {
      review: { loading: false, hasMore: false, loadingMore: false, error: "database is down", onLoadMore: () => {}, onRetry: () => {} },
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent("database is down");
  },
};
