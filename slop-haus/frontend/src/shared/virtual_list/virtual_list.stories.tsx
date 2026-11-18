import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { Button } from "@slop-factory/ui";
import VirtualList from "./virtual_list";
import type { LoadMoreMode } from "./virtual_list";

const TOTAL = 5000;
const PAGE = 100;

function ticketRows(count: number): string[] {
  return Array.from({ length: count }, (_, i) => `Ticket ${i + 1}`);
}

function Row({ title }: { title: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", padding: "8px 12px" }}>
      <span>{title}</span>
      <Button size="sm">Open</Button>
    </div>
  );
}

// Owns the loaded rows the way PagedCollection will: a load flips loading
// on, and the next page arrives from a resolved promise rather than a timer.
function PagedList({ mode, initial = PAGE }: { mode: LoadMoreMode; initial?: number }) {
  const [items, setItems] = useState(() => ticketRows(initial));
  const [loading, setLoading] = useState(false);

  const loadMore = () => {
    setLoading(true);
    void Promise.resolve().then(() => {
      setItems((current) => ticketRows(Math.min(current.length + PAGE, TOTAL)));
      setLoading(false);
    });
  };

  return (
    <div style={{ padding: 24, width: 520 }}>
      <VirtualList
        items={items}
        getKey={(title) => title}
        renderRow={(title) => <Row title={title} />}
        label="Tickets"
        estimateRowHeight={48}
        totalCount={TOTAL}
        hasMore={items.length < TOTAL}
        loading={loading}
        onLoadMore={loadMore}
        mode={mode}
      />
    </div>
  );
}

// No `component` here: Storybook's args typing cannot carry VirtualList's
// item type, and every story renders it through its own wrapper anyway.
const meta = {
  title: "Lists/VirtualList",
  parameters: { layout: "centered" },
} satisfies Meta;

export default meta;
type Story = StoryObj;

export const Thousands: Story = {
  render: () => (
    <div style={{ padding: 24, width: 520 }}>
      <VirtualList
        items={ticketRows(TOTAL)}
        getKey={(title) => title}
        renderRow={(title) => <Row title={title} />}
        label="Tickets"
        estimateRowHeight={48}
      />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const rows = canvas.getAllByRole("listitem");

    await expect(rows.length).toBeLessThan(40);
    await expect(rows[0]).toHaveAttribute("aria-posinset", "1");
    await expect(rows[0]).toHaveAttribute("aria-setsize", String(TOTAL));
  },
};

export const InfiniteScroll: Story = {
  render: () => <PagedList mode="infinite" />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const viewport = canvas.getByRole("region", { name: "Tickets" });

    viewport.scrollTop = viewport.scrollHeight;

    await waitFor(() => expect(canvas.getByRole("status")).toHaveTextContent(/more rows loaded/));
  },
};

export const LoadMoreButton: Story = {
  render: () => <PagedList mode="button" />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);

    await userEvent.click(canvas.getByRole("button", { name: "Load more" }));

    await waitFor(() => expect(canvas.getByRole("status")).toHaveTextContent("100 more rows loaded, 200 of 5000 shown"));
    await expect(canvas.getByRole("button", { name: "Load more" })).toBeEnabled();
  },
};

export const VariableHeights: Story = {
  render: () => (
    <div style={{ padding: 24, width: 520 }}>
      <VirtualList
        items={ticketRows(2000)}
        getKey={(title) => title}
        renderRow={(title, index) => (
          <div style={{ padding: "8px 12px" }}>
            <strong>{title}</strong>
            {index % 3 === 0 && <p style={{ margin: "4px 0 0" }}>A longer description that takes a second line of space.</p>}
            {index % 5 === 0 && <p style={{ margin: "4px 0 0" }}>And a third line, so no two rows agree on a height.</p>}
          </div>
        )}
        label="Tickets with descriptions"
        estimateRowHeight={60}
        measureRows
      />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const rows = canvas.getAllByRole("listitem");

    await expect(rows.length).toBeLessThan(40);
    await expect(rows[0]).toHaveAttribute("aria-setsize", "2000");
    // Row 0 has three lines and row 1 one: measured, not all the estimate.
    await expect(rows[0].offsetHeight).toBeGreaterThan(rows[1].offsetHeight);
    await expect(rows[1].getBoundingClientRect().top).toBeGreaterThanOrEqual(rows[0].getBoundingClientRect().bottom - 1);
  },
};
