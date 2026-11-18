import { describe, expect, it } from "vitest";
import type { Status } from "@/api";
import { forgetPersonalState } from "@/lib/personal_state";
import { columnContext, rememberBoardColumn, rememberedBoardColumn, shownColumn, visibleColumns } from "./board_columns";

const LIVE: Status[] = ["preparing", "open", "in_progress", "blocked", "discussion", "review"];
const WITH_DONE: Status[] = [...LIVE, "done"];
const WITH_PARKED: Status[] = ["parked", ...LIVE];

function tickets(...statuses: Status[]): { status: Status }[] {
  return statuses.map((status) => ({ status }));
}

describe("shownColumn", () => {
  it("shows the column asked for", () => {
    expect(shownColumn(tickets("in_progress"), LIVE, "blocked")).toBe("blocked");
  });

  // ?column=done from before "Include done" was unticked names a column
  // the board no longer has.
  it("ignores a column the board is not showing", () => {
    expect(shownColumn(tickets("open"), LIVE, "done")).toBe("open");
    expect(shownColumn(tickets("open"), WITH_DONE, "done")).toBe("done");
  });

  it("opens on In progress when it has tickets", () => {
    expect(shownColumn(tickets("preparing", "open", "in_progress", "review"), LIVE)).toBe("in_progress");
  });

  it("opens on the first column after In progress with tickets in it", () => {
    expect(shownColumn(tickets("preparing", "open", "discussion", "review"), LIVE)).toBe("discussion");
  });

  it("wraps round to the columns before In progress", () => {
    expect(shownColumn(tickets("open", "preparing"), LIVE)).toBe("preparing");
    expect(shownColumn(tickets("open"), LIVE)).toBe("open");
  });

  it("only opens on Done when it is shown", () => {
    expect(shownColumn(tickets("done", "open"), WITH_DONE)).toBe("done");
    expect(shownColumn(tickets("done", "open"), LIVE)).toBe("open");
  });

  it("shows Parked only while parked tickets are included, and never opens on it unasked", () => {
    expect(shownColumn(tickets("parked"), LIVE, "parked")).toBe("in_progress");
    expect(shownColumn(tickets("parked"), WITH_PARKED, "parked")).toBe("parked");
    expect(shownColumn(tickets("parked", "open"), WITH_PARKED)).toBe("parked");
  });

  it("opens on In progress when the board is empty", () => {
    expect(shownColumn([], LIVE)).toBe("in_progress");
  });
});

it("forgets the remembered column when the person signs out", () => {
  rememberBoardColumn("review");

  forgetPersonalState();

  expect(rememberedBoardColumn()).toBeUndefined();
});

describe("the In merge queue column", () => {
  const ALL: Status[] = [...LIVE.slice(0, 3), "in_merge_queue", ...LIVE.slice(3)];
  const names = (statuses: Status[], mergeQueueMode: boolean, ...held: Status[]) =>
    visibleColumns(statuses, columnContext(tickets(...held), mergeQueueMode)).map((c) => c.status);

  it("follows In progress when the tenant lands by the merge queue", () => {
    const shown = names(ALL, true);
    expect(shown.indexOf("in_merge_queue")).toBe(shown.indexOf("in_progress") + 1);
  });

  it("is absent in worker mode with nothing in it", () => {
    expect(names(ALL, false, "open")).not.toContain("in_merge_queue");
  });

  it("is present in worker mode while a ticket is still in it", () => {
    expect(names(ALL, false, "in_merge_queue")).toContain("in_merge_queue");
  });

  it("is absent when its status is not asked for, whatever the mode", () => {
    expect(names(LIVE, true)).not.toContain("in_merge_queue");
  });

  it("can be the column a phone opens on when it is the only one with a ticket", () => {
    expect(shownColumn(tickets("in_merge_queue"), ALL, undefined, false)).toBe("in_merge_queue");
  });
});
