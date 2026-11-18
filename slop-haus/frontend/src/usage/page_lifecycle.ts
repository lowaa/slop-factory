import type { ScreenTracker } from "./screen_time";

// Tells the tracker when the page is in front of the person: visible to the
// browser and with the window focused. Time is counted only then.
//
// Hidden (a tab switch, a minimise, the page going away) also flushes: it is
// the last event a closing tab reliably gets, and `pagehide` covers the
// browsers that don't fire `visibilitychange` on a close. Coming back
// (`visibilitychange` to visible, or `pageshow` from the back/forward cache,
// where the page was frozen with the clock stopped) resumes it.
export function watchPage(tracker: ScreenTracker, win: Window, doc: Document): () => void {
  const inFront = () => doc.visibilityState === "visible" && doc.hasFocus();
  const refresh = () => tracker.setVisible(inFront());
  const onVisibility = () => (doc.visibilityState === "hidden" ? tracker.pageHidden() : refresh());

  tracker.setVisible(inFront());
  doc.addEventListener("visibilitychange", onVisibility);
  const onPageHide = () => tracker.pageHidden();
  win.addEventListener("pagehide", onPageHide);
  win.addEventListener("pageshow", refresh);
  win.addEventListener("focus", refresh);
  win.addEventListener("blur", refresh);

  return () => {
    doc.removeEventListener("visibilitychange", onVisibility);
    win.removeEventListener("pagehide", onPageHide);
    win.removeEventListener("pageshow", refresh);
    win.removeEventListener("focus", refresh);
    win.removeEventListener("blur", refresh);
  };
}
