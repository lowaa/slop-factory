import { useEffect, useMemo, type ReactNode } from "react";
import type { SlopHausRouter } from "@/routes";
import { watchPage } from "./page_lifecycle";
import { screenOfRoute } from "./screens";
import { ScreenTracker } from "./screen_time";
import { sendUsage } from "./send";
import { Usage, type UsageSender } from "./usage";
import { UsageContext } from "./use_usage";

interface UsageProviderProps {
  router: SlopHausRouter;
  // Usage is reported for signed-in users only; otherwise nothing is counted.
  enabled: boolean;
  // Replaced in tests, which have no server.
  send?: UsageSender;
  children: ReactNode;
}

// Reports the screens as the router moves between them, and the time spent on
// each. Inside the router's context, around the routed tree.
export function UsageProvider({ router, enabled, send = sendUsage, children }: UsageProviderProps) {
  const usage = useMemo(() => new Usage(send), [send]);

  useEffect(() => {
    if (!enabled) return;
    const tracker = new ScreenTracker(usage, () => performance.now());
    const enterCurrentScreen = () => tracker.enter(screenOfRoute(router.state.matches.at(-1)?.fullPath));
    const stopWatchingPage = watchPage(tracker, window, document);
    enterCurrentScreen();
    const stopWatchingRoutes = router.subscribe("onResolved", enterCurrentScreen);
    return () => {
      stopWatchingRoutes();
      stopWatchingPage();
      // Sends the last screen's time; a sign-out or unmount is a visit's end.
      tracker.pageHidden();
      tracker.enter(null);
    };
  }, [usage, router, enabled]);

  return <UsageContext.Provider value={usage}>{children}</UsageContext.Provider>;
}
