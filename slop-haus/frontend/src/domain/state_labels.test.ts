import { describe, expect, it } from "vitest";
import { sessionLabel } from "./state_labels";

describe("sessionLabel", () => {
  it.each([
    ["ready", false, "Offline"],
    ["starting", false, "Offline"],
    ["ready", true, "Connected"],
    ["starting", true, "Connecting"],
    ["stopped", false, "Disconnected"],
    ["failed", false, "Failed"],
    ["ready", null, "Connected"],
  ] as const)("%s with host online %s reads %s", (state, hostOnline, label) => {
    expect(sessionLabel(state, hostOnline)).toBe(label);
  });
});
