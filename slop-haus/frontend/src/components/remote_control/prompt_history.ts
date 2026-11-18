export const MAX_PROMPTS = 10;

export interface PromptHistorySnapshot {
  // Oldest first, so the newest is last and drawn at the bottom of the list.
  readonly prompts: readonly string[];
  readonly open: boolean;
  readonly highlighted: number;
  // What the box held when the list opened.
  readonly draft: string;
}

// What the view does with the key or click it forwarded: "pass" leaves the
// browser's default (the cursor moves), "handled" swallows it, and "fill"
// swallows it and puts the text in the box.
export type PromptHistoryAction = { kind: "pass" } | { kind: "handled" } | { kind: "fill"; text: string };

const PASS: PromptHistoryAction = { kind: "pass" };
const HANDLED: PromptHistoryAction = { kind: "handled" };

// The newest MAX_PROMPTS distinct prompts, a repeat counting where it was last
// said.
export function boundedPrompts(all: readonly string[]): string[] {
  const seen = new Set<string>();
  const newestFirst: string[] = [];
  for (let i = all.length - 1; i >= 0 && newestFirst.length < MAX_PROMPTS; i--) {
    const text = all[i];
    if (text.trim() === "" || seen.has(text)) continue;
    seen.add(text);
    newestFirst.push(text);
  }
  return newestFirst.reverse();
}

// PromptHistory is the state of a composer's shell-style prompt history, kept
// outside React so every transition can be tested without rendering. The view
// forwards intents and draws the snapshot, via useSyncExternalStore.
export class PromptHistory {
  private snapshot: PromptHistorySnapshot = { prompts: [], open: false, highlighted: 0, draft: "" };
  private readonly listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = (): PromptHistorySnapshot => this.snapshot;

  // Replaces the prompts with those the conversation holds. An open list
  // keeps its highlight on the same prompt, or the newest if that one is gone.
  setPrompts(all: readonly string[]): void {
    const prompts = boundedPrompts(all);
    const { prompts: old } = this.snapshot;
    if (prompts.length === old.length && prompts.every((p, i) => p === old[i])) return;
    const kept = prompts.indexOf(old[this.snapshot.highlighted]);
    this.update({
      prompts,
      open: this.snapshot.open && prompts.length > 0,
      highlighted: kept >= 0 ? kept : Math.max(prompts.length - 1, 0),
    });
  }

  promptSent(text: string): void {
    this.setPrompts([...this.snapshot.prompts, text]);
  }

  // Up on the top line opens the list at the newest prompt; once open, Up
  // moves the highlight and stays at the oldest. Off the top line, or with
  // nothing to show, the key is the cursor's.
  keyUp({ onTopLine, draft }: { onTopLine: boolean; draft: string }): PromptHistoryAction {
    const s = this.snapshot;
    if (s.open) {
      this.update({ highlighted: Math.max(s.highlighted - 1, 0) });
      return HANDLED;
    }
    if (!onTopLine || s.prompts.length === 0) return PASS;
    this.update({ open: true, highlighted: s.prompts.length - 1, draft });
    return HANDLED;
  }

  keyDown(): PromptHistoryAction {
    const s = this.snapshot;
    if (!s.open) return PASS;
    this.update({ highlighted: Math.min(s.highlighted + 1, s.prompts.length - 1) });
    return HANDLED;
  }

  // Chooses the highlighted prompt, or the one clicked.
  select(index: number = this.snapshot.highlighted): PromptHistoryAction {
    const s = this.snapshot;
    if (!s.open) return PASS;
    const text = s.prompts[index];
    if (text === undefined) return HANDLED;
    this.update({ open: false, highlighted: s.prompts.length - 1, draft: "" });
    return { kind: "fill", text };
  }

  cancel(): PromptHistoryAction {
    if (!this.snapshot.open) return PASS;
    this.update({ open: false, draft: "" });
    return HANDLED;
  }

  private update(change: Partial<PromptHistorySnapshot>): void {
    this.snapshot = { ...this.snapshot, ...change };
    for (const l of [...this.listeners]) l();
  }
}
