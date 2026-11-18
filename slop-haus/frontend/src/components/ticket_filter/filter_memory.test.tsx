import { afterEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { forgetPersonalState } from "@/lib/personal_state";
import { forgetRememberedFilters, rememberFilterSearch, rememberedFilterSearch } from "./filter_memory";
import TicketFilter from "./ticket_filter";
import { TicketFilterModel } from "@/domain/ticket_filter_model";

const fields = [
  { type: "text", id: "text", param: "q", label: "Search" },
  { type: "flag", id: "done", param: "done", label: "Include done" },
] as const;
const ONE = new TicketFilterModel("one", fields);
const TWO = new TicketFilterModel("two", fields);

afterEach(() => forgetRememberedFilters());

describe("the filter memory", () => {
  it("keeps each instance's filter apart from the others'", () => {
    render(
      <>
        <TicketFilter model={ONE} values={{ text: "alpha", done: true }} onChange={() => {}} />
        <TicketFilter model={TWO} values={{ text: "beta", done: false }} onChange={() => {}} />
      </>,
    );

    expect(rememberedFilterSearch("one")).toEqual({ q: "alpha", done: true });
    expect(rememberedFilterSearch("two")).toEqual({ q: "beta" });
  });

  it("remembers a cleared filter as well as a set one, with the page's own address parts", () => {
    const { rerender } = render(
      <TicketFilter model={ONE} values={{ text: "alpha", done: false }} onChange={() => {}} extraSearch={{ after: "c2" }} />,
    );
    expect(rememberedFilterSearch("one")).toEqual({ q: "alpha", after: "c2" });

    rerender(<TicketFilter model={ONE} values={{ text: "", done: false }} onChange={() => {}} />);

    expect(rememberedFilterSearch("one")).toEqual({});
  });

  it("forgets everything on request, and has nothing for an instance never shown", () => {
    render(<TicketFilter model={ONE} values={{ text: "alpha", done: false }} onChange={() => {}} />);

    forgetRememberedFilters();

    expect(rememberedFilterSearch("one")).toBeUndefined();
    expect(rememberedFilterSearch("two")).toBeUndefined();
  });
});

it("forgets remembered filters when the person signs out", () => {
  rememberFilterSearch("k", { q: "secret" });

  forgetPersonalState();

  expect(rememberedFilterSearch("k")).toBeUndefined();
});
