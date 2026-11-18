import { useId, useState } from "react";
import type { Status } from "@/api";
import { Button, Dropdown } from "@slop-factory/ui";
import { useNarrowLayout } from "@/lib/layout";
import { SORTS, type SortId, type SortModel } from "@/lib/ticket_query/ticket_sort";
import { TicketJump } from "@/components/ticket_jump";
import { useTicketJump } from "@/components/ticket_jump";
import { statusLabel } from "@/domain/board_columns";
import { useTicketFilter, type TicketFilterOptions } from "./use_ticket_filter";
import {
  FILTER_PRIORITIES,
  FILTER_STATUSES,
  type DateRangeFieldSpec,
  type FieldSpec,
  type FlagFieldSpec,
  type PriorityFieldSpec,
  type StatusFieldSpec,
  type TextFieldSpec,
} from "@/domain/ticket_filter_model";
import "./ticket_filter.scss";

// "Any" is a real option whose value is "", the filter's own unset value.
const PRIORITY_OPTIONS = [{ value: "", label: "Any" }, ...FILTER_PRIORITIES.map((p) => ({ value: p, label: p }))];

type Controller = ReturnType<typeof useTicketFilter>;

export interface TicketFilterProps extends TicketFilterOptions {
  // Names the group for assistive technology; a page with another group of
  // filters of its own tells the two apart.
  legend?: string;
}

// TicketFilter is the filter's view: one control per field the model
// lists, in order. The values and what to do with a change are the page's.
export default function TicketFilter({ legend = "Filters", ...options }: TicketFilterProps) {
  const { model } = options;
  const controller = useTicketFilter(options);
  const narrow = useNarrowLayout();
  // Not remembered: a phone starts with the fields tucked away on every
  // visit, however the filter came to be set.
  const [open, setOpen] = useState(false);
  const fieldsId = useId();

  const fields = (
    <>
      {model.fields.map((spec) => (
        <Field key={spec.id} spec={spec} options={options} controller={controller} />
      ))}
      {options.sort && <SortField model={options.sort.model} value={options.sort.value} onChange={options.sort.onChange} />}
    </>
  );
  const clear = controller.filtered && (
    <Button className="ticket-filter__clear" onClick={controller.clear}>
      Clear filters
    </Button>
  );

  if (!narrow) {
    return (
      <fieldset className="ticket-filter">
        <legend className="ticket-filter__legend">{legend}</legend>
        {fields}
        {clear}
      </fieldset>
    );
  }
  return (
    <fieldset className="ticket-filter ticket-filter--collapsible">
      <legend className="ticket-filter__legend">{legend}</legend>
      <div className="ticket-filter__bar">
        <button
          type="button"
          className="ticket-filter__toggle"
          aria-expanded={open}
          aria-controls={fieldsId}
          onClick={() => setOpen(!open)}
        >
          {controller.activeCount > 0 ? `${legend} \u00b7 ${controller.activeCount}` : legend}
          <span aria-hidden="true" className="ticket-filter__chevron">
            {open ? "\u25b4" : "\u25be"}
          </span>
        </button>
        {clear}
      </div>
      {/* Hidden, not unmounted, so aria-controls always names an element. */}
      <div id={fieldsId} className="ticket-filter__fields" hidden={!open}>
        {fields}
      </div>
    </fieldset>
  );
}

function Field({ spec, options, controller }: { spec: FieldSpec; options: TicketFilterOptions; controller: Controller }) {
  const { model, values } = options;
  switch (spec.type) {
    case "text":
      return <TextField spec={spec} controller={controller} />;
    case "flag":
      return <FlagField spec={spec} checked={model.flag(values, spec.id)} controller={controller} />;
    case "status":
      return <StatusField spec={spec} excluded={model.excluded(values, spec.id)} controller={controller} />;
    case "priority":
      return <PriorityField spec={spec} value={model.priority(values, spec.id)} controller={controller} />;
    case "date_range":
      return <DateRangeField spec={spec} range={model.range(values, spec.id)} controller={controller} />;
  }
}

function TextField({ spec, controller }: { spec: TextFieldSpec; controller: Controller }) {
  const { draft, setDraft } = controller;
  // "#85" still filters like any other text -- the server also matches it
  // to ticket 85 -- and the jump is offered on top of that. Not offered
  // where the field doesn't ask for it.
  const jump = useTicketJump(spec.ticketJump ? draft : "");
  return (
    <div className="ticket-filter__search">
      <input
        type="search"
        className="ticket-filter__text"
        aria-label={spec.label}
        placeholder={spec.placeholder}
        maxLength={200}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          // Enter that ends an IME composition is the composition's, not a jump.
          if (e.nativeEvent.isComposing) return;
          // Only when there is an offer to act on: otherwise Escape keeps
          // the search box's own meaning, clearing it.
          if ((e.key === "Enter" && jump.open()) || (e.key === "Escape" && jump.dismiss())) {
            e.preventDefault();
          }
        }}
      />
      {spec.ticketJump && <TicketJump jump={jump} />}
    </div>
  );
}

function FlagField({ spec, checked, controller }: { spec: FlagFieldSpec; checked: boolean; controller: Controller }) {
  return (
    <label className="ticket-filter__option">
      <input type="checkbox" checked={checked} onChange={(e) => controller.setField(spec.id, e.target.checked)} />
      {spec.label}
    </label>
  );
}

function StatusField({
  spec,
  excluded,
  controller,
}: {
  spec: StatusFieldSpec;
  excluded: readonly Status[];
  controller: Controller;
}) {
  const shown = FILTER_STATUSES.filter((s) => !excluded.includes(s));
  return (
    <details className="ticket-filter__statuses">
      <summary>Statuses ({shown.length === FILTER_STATUSES.length ? "all" : `${shown.length} of ${FILTER_STATUSES.length}`})</summary>
      <div className="ticket-filter__status-list">
        {FILTER_STATUSES.map((status) => (
          <label key={status} className="ticket-filter__option">
            <input
              type="checkbox"
              checked={!excluded.includes(status)}
              onChange={(e) =>
                controller.setField(
                  spec.id,
                  e.target.checked ? excluded.filter((s) => s !== status) : [...excluded, status],
                )
              }
            />
            {statusLabel(status)}
          </label>
        ))}
      </div>
    </details>
  );
}

function PriorityField({
  spec,
  value,
  controller,
}: {
  spec: PriorityFieldSpec;
  value: string | undefined;
  controller: Controller;
}) {
  return (
    <div className="ticket-filter__field">
      <Dropdown<string>
        label="Priority"
        value={value ?? ""}
        options={PRIORITY_OPTIONS}
        onChange={(next) => controller.setField(spec.id, next)}
      />
    </div>
  );
}

function DateRangeField({
  spec,
  range,
  controller,
}: {
  spec: DateRangeFieldSpec;
  range: { from: string; to: string };
  controller: Controller;
}) {
  return (
    <>
      <label className="ticket-filter__field">
        {spec.label} from
        <input
          type="date"
          value={range.from}
          max={range.to || undefined}
          onChange={(e) => controller.setField(spec.id, { ...range, from: e.target.value })}
        />
      </label>
      <label className="ticket-filter__field">
        {spec.label} to
        <input
          type="date"
          value={range.to}
          min={range.from || undefined}
          onChange={(e) => controller.setField(spec.id, { ...range, to: e.target.value })}
        />
      </label>
    </>
  );
}

function SortField({ model, value, onChange }: { model: SortModel; value: SortId; onChange: (sort: SortId) => void }) {
  // One choice is no choice.
  if (model.offered.length < 2) return null;
  return (
    <div className="ticket-filter__field">
      <Dropdown<SortId>
        label="Sort by"
        value={value}
        options={model.offered.map((id) => ({ value: id, label: SORTS[id].label }))}
        onChange={onChange}
      />
    </div>
  );
}
