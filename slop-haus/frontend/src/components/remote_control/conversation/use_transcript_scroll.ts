import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { ConversationEvent } from "@/api";
import { lastSeq } from "@/domain/transcript";

// How far from the end still counts as being at it, in px: sub-pixel scroll
// positions and a line of growth should not unstick the view.
const BOTTOM_SLACK = 24;

// useTranscriptScroll keeps the transcript's scroll position right as events
// arrive:
//  - older turns inserted above leave the content in view where it was, which
//    CSS overflow-anchor alone does not promise (it is absent in Safari, and
//    is turned off by the offscreen rendering the turns use);
//  - new output follows the end only while the person is at the end; scrolled
//    up, the view stays put and jumpToLatest is offered.
// Both are done in a layout effect, so the browser never paints the jump.
//
// loadingOlder is the spinner above the transcript, which changes its height
// in a commit of its own: the height a later prepend is measured against has
// to be taken with it.
export function useTranscriptScroll(events: ConversationEvent[] | null, loadingOlder: boolean) {
  const listRef = useRef<HTMLOListElement>(null);
  const atBottom = useRef(true);
  const seen = useRef<{ height: number; first: number; last: number } | null>(null);
  const [hasNewOutput, setHasNewOutput] = useState(false);

  const onScroll = useCallback(() => {
    const el = listRef.current;
    if (!el) return;
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK;
    if (atBottom.current) setHasNewOutput(false);
  }, []);

  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el || !events) return;
    const first = events[0]?.seq ?? 0;
    const last = lastSeq(events);
    const was = seen.current;
    if (was && first < was.first) {
      el.scrollTop += el.scrollHeight - was.height;
    }
    if (!was || last > was.last) {
      if (atBottom.current) {
        el.scrollTop = el.scrollHeight;
      } else if (was) {
        setHasNewOutput(true);
      }
    }
    seen.current = { height: el.scrollHeight, first, last };
  }, [events, loadingOlder]);

  const jumpToLatest = useCallback(() => {
    const el = listRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    atBottom.current = true;
    setHasNewOutput(false);
  }, []);

  return { listRef, onScroll, hasNewOutput, jumpToLatest };
}
