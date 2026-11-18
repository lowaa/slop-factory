import { describe, expect, it } from "vitest";
import { lazyRoute, preloadRoutes } from "./lazy_route";

describe("preloadRoutes", () => {
  it("starts every registered loader and resolves only once all have loaded", async () => {
    const releases: Array<() => void> = [];
    const started: string[] = [];
    // One promise per loader: a later preloadRoutes (another test file sharing
    // this module registry) must not start a load nobody releases.
    const slowLoader = (name: string) => {
      let loaded: Promise<{ default: () => null }> | undefined;
      return () =>
        (loaded ??= new Promise((resolve) => {
          started.push(name);
          releases.push(() => resolve({ default: () => null }));
        }));
    };
    lazyRoute(slowLoader("a"));
    lazyRoute(slowLoader("b"));

    let done = false;
    const preloading = preloadRoutes().then(() => (done = true));
    expect(started).toEqual(["a", "b"]);
    releases[0]();
    await Promise.resolve();
    await Promise.resolve();
    expect(done).toBe(false);
    releases[1]();
    await preloading;
    expect(done).toBe(true);
  });
});

describe("route modules", () => {
  it("are all lazy-loaded through lazyRoute, or preloadRoutes would miss them", () => {
    const sources = import.meta.glob(
      [
        "../../**/*.{ts,tsx}",
        "!../../**/*.{test,stories}.*",
        "!./lazy_route.ts",
      ],
      {
        query: "?raw",
        import: "default",
        eager: true,
      },
    );
    const bare = Object.entries(sources)
      .filter(([, text]) =>
        /import \{[^}]*\blazy\b[^}]*\} from "react"/.test(text),
      )
      .map(([file]) => file);
    expect(bare).toEqual([]);
  });
});
