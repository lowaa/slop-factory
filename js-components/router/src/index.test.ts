import { describe, expect, it } from "vitest";
import * as router from "./index";

// Apps only ever import the package entry, so a helper that is not exported
// from it is not available to them however well its own tests pass.
describe("package entry", () => {
  it.each([
    "getRouteApi",
    "linkOptions",
    "optionalOneOf",
    "positiveIntParam",
    "uuidParam",
    "createAppRouter",
    "redirectTo",
    "loginPathReturningTo",
    "returnToSearch",
  ] as const)("exports %s", (name) => {
    expect(typeof router[name]).toBe("function");
  });

  // Present is not enough: an entry exporting the wrong helper under this
  // name would pass the check above.
  it("exports uuidParam as the UUID check", () => {
    const { parse } = router.uuidParam("id");
    expect(parse({ id: "0f8fad5b-d9cb-469f-a165-70867728950e" })).toEqual({ id: "0f8fad5b-d9cb-469f-a165-70867728950e" });
    expect(parse({ id: "42" })).toBe(false);
  });

  it("exports the Storybook router provider at its own subpath", async () => {
    const provider = await import("@slop-factory/router/testing/provider");
    expect(typeof provider.TestRouterProvider).toBe("function");
  });
});
