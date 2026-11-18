import { beforeEach, describe, expect, it } from "vitest";
import { forgetPersonalState } from "@/lib/personal_state";
import { captureDeviceParams, clearPendingDevice, hasPendingDeviceVisit } from "./pending_device";

function visit(query: string) {
  window.history.replaceState({}, "", `/device${query}`);
  return captureDeviceParams();
}

describe("captureDeviceParams supervisor hint", () => {
  beforeEach(() => clearPendingDevice());

  it("keeps the hint through a query-less return leg", () => {
    expect(visit("?kind=supervisor&slots=4").supervisor).toEqual({ slots: 4 });
    expect(visit("").supervisor).toEqual({ slots: 4 });
  });

  it("does not complete a later link's half pair from an earlier link", () => {
    visit("?kind=supervisor&slots=4");

    expect(visit("?kind=supervisor").supervisor).toBeNull();
  });

  it("replaces an earlier hint with the later link's", () => {
    visit("?kind=supervisor&slots=4");

    expect(visit("?kind=supervisor&slots=9").supervisor).toEqual({ slots: 9 });
  });
});

it("is forgotten when the person signs out", () => {
  visit("?code=BCDF-GHJK&agent=b&kind=supervisor&slots=4");
  window.history.replaceState({}, "", "/");
  expect(hasPendingDeviceVisit()).toBe(true);

  forgetPersonalState();

  expect(hasPendingDeviceVisit()).toBe(false);
  const next = visit("");
  expect(next.code).toBeNull();
  expect(next.agent).toBeNull();
  expect(next.supervisor).toBeNull();
});
