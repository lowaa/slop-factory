import type { ConversationEvent, ConversationEventsPage } from "@/api";

export const NO_EVENTS: ConversationEventsPage = { events: [], hasMore: false, nextBefore: 0, prompts: [] };

// Stands in for the events endpoint's backward read over a fixed transcript:
// the last `turns` turns (a user event and what follows it) before `before`,
// and on the first read the user messages as the prompts. It follows the
// documented contract only; the rule itself is the server's to test.
export function recentEventsOf(history: ConversationEvent[]) {
  return async (_conductor: string, _id: string, { before, turns }: { before: number; turns: number }): Promise<ConversationEventsPage> => {
    const upto = history.filter((e) => before === 0 || e.seq < before);
    const starts = upto.flatMap((e, i) => (e.kind === "user" ? [i] : []));
    const count = Math.max(turns, 1);
    const from = starts.length > count ? starts[starts.length - count] : 0;
    const events = upto.slice(from);
    return {
      events,
      hasMore: from > 0,
      nextBefore: from > 0 ? events[0].seq : 0,
      prompts: before === 0 ? history.filter((e) => e.kind === "user").map((e) => e.body) : null,
    };
  };
}
