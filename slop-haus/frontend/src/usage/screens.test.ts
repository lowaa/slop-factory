import { describe, expect, it } from "vitest";
import { createSlopHausRouter } from "@/routes";
import { SCREENS } from "./catalog";
import { TRACKED_ROUTES, UNTRACKED_ROUTES, screenOfRoute } from "./screens";

describe("screens", () => {
  it("places every route of the app as a screen or as deliberately not one", () => {
    const routes = Object.keys(createSlopHausRouter().routesByPath).sort();
    expect(routes).toEqual([...TRACKED_ROUTES, ...UNTRACKED_ROUTES].sort());
  });

  it("gives every catalog screen a route, and no route a screen the catalog lacks", () => {
    const used = new Set(TRACKED_ROUTES.map((r) => screenOfRoute(r)));
    expect([...used].sort()).toEqual([...SCREENS].sort());
  });

  it("reads an index route's trailing slash as the route", () => {
    expect(screenOfRoute("/reports/")).toBe("reports");
    expect(screenOfRoute("/")).toBe("board");
  });

  it("is by template, so an id never names a screen", () => {
    expect(screenOfRoute("/tickets/$ticketId")).toBe("ticket");
    expect(screenOfRoute("/tickets/42")).toBeNull();
  });

  it("says nothing for the sign-in pages or an unknown route", () => {
    expect(screenOfRoute("/login")).toBeNull();
    expect(screenOfRoute("/$")).toBeNull();
    expect(screenOfRoute(undefined)).toBeNull();
  });
});
