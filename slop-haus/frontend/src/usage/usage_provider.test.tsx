import { act, cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SlopHausRouter } from "@/routes";
import type { UsageEvent } from "./usage";
import { UsageProvider } from "./usage_provider";
import { useUsage } from "./use_usage";

// The part of the router the provider reads: where it is, and when it moves.
function fakeRouter(start: string) {
  const listeners = new Set<() => void>();
  const router = {
    state: { matches: [{ fullPath: start }] },
    subscribe: (_event: string, fn: () => void) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
  return {
    router: router as unknown as SlopHausRouter,
    goTo(path: string) {
      router.state.matches = [{ fullPath: path }];
      listeners.forEach((fn) => fn());
    },
  };
}

function Feature() {
  const usage = useUsage();
  return <button onClick={() => usage.event("new_ticket", { action: "open_form" })}>use</button>;
}

afterEach(() => cleanup());

describe("UsageProvider", () => {
  it("reports each screen on route resolve and the feature events on it, sent when the page hides", () => {
    const sent: UsageEvent[][] = [];
    const { router, goTo } = fakeRouter("/");
    const { getByText } = render(
      <UsageProvider router={router} enabled send={(events) => sent.push(events)}>
        <Feature />
      </UsageProvider>,
    );
    act(() => getByText("use").click());
    act(() => goTo("/tickets/$ticketId"));
    act(() => goTo("/login"));
    expect(sent).toEqual([]);

    act(() => void window.dispatchEvent(new Event("pagehide")));

    expect(sent.flat().map((e) => [e.name, e.attrs.screen ?? null])).toEqual([
      ["screen_view", "board"],
      ["new_ticket", "board"],
      ["screen_view", "ticket"],
    ]);
  });

  it("counts nothing while not enabled, as for a signed-out visitor", () => {
    const send = vi.fn();
    const { router, goTo } = fakeRouter("/");
    const { getByText } = render(
      <UsageProvider router={router} enabled={false} send={send}>
        <Feature />
      </UsageProvider>,
    );
    act(() => getByText("use").click());
    act(() => goTo("/reports"));
    act(() => void window.dispatchEvent(new Event("pagehide")));
    expect(send).not.toHaveBeenCalled();
  });
});
