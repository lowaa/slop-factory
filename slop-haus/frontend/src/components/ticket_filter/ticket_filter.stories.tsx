import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { APPROVALS_FILTER_MODEL } from "@/domain/approvals_filter";
import { BOARD_FILTER_MODEL } from "@/domain/board_filter";
import { UNREVIEWED_FILTER_MODEL, UNREVIEWED_SORT_MODEL } from "@/domain/unreviewed_filter";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import TicketFilter from "./ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";

const meta = {
  title: "Tickets/TicketFilter",
  component: TicketFilter,
  args: { model: BOARD_FILTER_MODEL, values: BOARD_FILTER_MODEL.defaults(), onChange: fn() },
  // The page owns the values; without an owner a ticked box would snap back.
  render: (args) => <Controlled {...args} />,
} satisfies Meta<typeof TicketFilter>;

export default meta;
type Story = StoryObj<typeof meta>;

function Controlled({ model, values, onChange }: React.ComponentProps<typeof TicketFilter>) {
  const [current, setCurrent] = useState(values);
  return (
    <TicketFilter
      model={model}
      values={current}
      onChange={(next: FilterValues) => {
        setCurrent(next);
        onChange(next);
      }}
    />
  );
}

export const BoardEmpty: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("");
    await expect(canvas.getByRole("checkbox", { name: "Include done" })).not.toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "Include parked" })).not.toBeChecked();
    await expect(canvas.queryByRole("button", { name: "Clear filters" })).toBeNull();
  },
};

export const BoardSearchText: Story = {
  args: { values: { ...BOARD_FILTER_MODEL.defaults(), text: "login bug" } },
  play: async ({ canvasElement, args }) => {
    const box = within(canvasElement).getByRole("searchbox", { name: "Search tickets" });
    await expect(box).toHaveValue("login bug");
    await userEvent.type(box, " today");
    // The text is handed on once typing pauses, not per keystroke.
    await waitFor(() =>
      expect(args.onChange).toHaveBeenLastCalledWith({ ...BOARD_FILTER_MODEL.defaults(), text: "login bug today" }),
    );
    await expect(args.onChange).toHaveBeenCalledTimes(1);
  },
};

export const BoardIncludeDone: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("checkbox", { name: "Include done" }));
    await expect(canvas.getByRole("checkbox", { name: "Include done" })).toBeChecked();
    await expect(args.onChange).toHaveBeenLastCalledWith({ ...BOARD_FILTER_MODEL.defaults(), done: true });
  },
};

export const BoardClear: Story = {
  args: { values: { text: "auth", done: true, parked: true } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Clear filters" }));
    await expect(args.onChange).toHaveBeenLastCalledWith(BOARD_FILTER_MODEL.defaults());
    await expect(canvas.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("");
  },
};

export const UnreviewedStatusAndDates: Story = {
  args: { model: UNREVIEWED_FILTER_MODEL, values: UNREVIEWED_FILTER_MODEL.defaults() },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Statuses (8 of 10)")).toBeVisible();
    await userEvent.click(canvas.getByText(/^Statuses/));
    await expect(canvas.getByRole("checkbox", { name: "Parked" })).not.toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "Done" })).toBeChecked();

    await userEvent.click(canvas.getByRole("checkbox", { name: "Parked" }));
    await expect(args.onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ status: ["cancelled"] }),
    );

    await userEvent.type(canvas.getByLabelText("Merged from"), "2026-10-01");
    await expect(args.onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ closed: { from: "2026-10-01", to: "" } }),
    );
  },
};

function WithSort({ onSort }: { onSort: (sort: SortId) => void }) {
  const [values, setValues] = useState(UNREVIEWED_FILTER_MODEL.defaults());
  const [sort, setSort] = useState<SortId>(UNREVIEWED_SORT_MODEL.defaultSort);
  return (
    <TicketFilter
      model={UNREVIEWED_FILTER_MODEL}
      values={values}
      onChange={setValues}
      sort={{ model: UNREVIEWED_SORT_MODEL, value: sort, onChange: (next) => { setSort(next); onSort(next); } }}
    />
  );
}

export const WithSortControl: Story = {
  render: (args) => <WithSort onSort={(sort) => args.onChange({ sort })} />,
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("combobox", { name: "Sort by" })).toHaveTextContent("Recently closed or merged");
    await userEvent.click(canvas.getByRole("combobox", { name: "Sort by" }));
    await userEvent.click(within(document.body).getByRole("option", { name: /^Priority/ }));
    await expect(args.onChange).toHaveBeenLastCalledWith({ sort: "priority" });
    // A chosen sort is not a filter.
    await expect(canvas.queryByRole("button", { name: "Clear filters" })).toBeNull();
  },
};

// Under 720px the filter is one row that opens the fields below it. The
// Approvals model has the most fields.
const phone = { viewport: { value: "mobile1", isRotated: false } };
const approvals = { model: APPROVALS_FILTER_MODEL };

export const PhoneCollapsed: Story = {
  globals: phone,
  args: { ...approvals, values: APPROVALS_FILTER_MODEL.defaults() },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    const toggle = canvas.getByRole("button", { name: "Filters" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(toggle.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    await expect(canvas.queryByRole("searchbox")).toBeNull();
    await expect(canvas.queryByRole("button", { name: "Clear filters" })).toBeNull();
  },
};

export const PhoneCollapsedFiltered: Story = {
  globals: phone,
  args: {
    ...approvals,
    values: { ...APPROVALS_FILTER_MODEL.defaults(), priority: "high", created: { from: "2026-10-01", to: "" } },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Filters \u00b7 2" })).toHaveAttribute("aria-expanded", "false");
    const clear = canvas.getByRole("button", { name: "Clear filters" });
    await expect(clear).toBeVisible();
    // Beside the toggle, not under it.
    const toggle = canvas.getByRole("button", { name: "Filters \u00b7 2" });
    const middle = (el: HTMLElement) => el.getBoundingClientRect().top + el.getBoundingClientRect().height / 2;
    await expect(Math.abs(middle(clear) - middle(toggle))).toBeLessThan(4);
  },
};

export const PhoneOpen: Story = {
  globals: phone,
  args: { ...approvals, values: APPROVALS_FILTER_MODEL.defaults() },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const toggle = canvas.getByRole("button", { name: "Filters" });
    await userEvent.click(toggle);
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(toggle).toHaveFocus();
    await expect(canvas.getByRole("searchbox", { name: "Title or text" })).toBeVisible();
    await expect(canvas.getByLabelText("Created from")).toBeVisible();
    // Fields are one column of finger-sized controls.
    const fields = [canvas.getByRole("searchbox"), canvas.getByRole("combobox", { name: "Priority" }), canvas.getByLabelText("Created from")];
    for (const field of fields) await expect(field.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    await expect(canvas.getByRole("combobox", { name: "Priority" }).getBoundingClientRect().top).toBeGreaterThan(
      canvas.getByRole("searchbox").getBoundingClientRect().bottom - 1,
    );
    await userEvent.click(canvas.getByRole("combobox", { name: "Priority" }));
    await userEvent.click(within(document.body).getByRole("option", { name: "high" }));
    await expect(canvas.getByRole("button", { name: "Filters \u00b7 1" })).toBeVisible();
  },
};
