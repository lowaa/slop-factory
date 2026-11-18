import { describe, expect, it, vi } from "vitest";
import { MAX_PROMPTS, PromptHistory } from "./prompt_history";

function withPrompts(...prompts: string[]): PromptHistory {
  const h = new PromptHistory();
  h.setPrompts(prompts);
  return h;
}

const top = (draft = "") => ({ onTopLine: true, draft });

describe("PromptHistory", () => {
  describe("the prompts", () => {
    it("keeps the newest ten, oldest first", () => {
      const all = Array.from({ length: 13 }, (_, i) => `p${i}`);
      const h = withPrompts(...all);

      expect(h.getSnapshot().prompts).toEqual(all.slice(3));
      expect(h.getSnapshot().prompts).toHaveLength(MAX_PROMPTS);
    });

    it("collapses exact repeats to where they were last said", () => {
      const h = withPrompts("a", "b", "a", "c");

      expect(h.getSnapshot().prompts).toEqual(["b", "a", "c"]);
    });

    it("counts the ten after collapsing, not before", () => {
      const h = withPrompts(...Array.from({ length: 10 }, (_, i) => `p${i}`), "p0", "p0");

      expect(h.getSnapshot().prompts).toEqual([...Array.from({ length: 9 }, (_, i) => `p${i + 1}`), "p0"]);
    });

    it("promptSent adds the newest, moving a repeat to the end", () => {
      const h = withPrompts("a", "b");
      h.promptSent("c");
      h.promptSent("a");

      expect(h.getSnapshot().prompts).toEqual(["b", "c", "a"]);
    });

    it("ignores blank prompts", () => {
      expect(withPrompts("a", "  ", "").getSnapshot().prompts).toEqual(["a"]);
    });
  });

  describe("keyUp", () => {
    it("opens at the newest prompt from the top line, taking the key", () => {
      const h = withPrompts("a", "b", "c");

      expect(h.keyUp(top("my draft"))).toEqual({ kind: "handled" });

      expect(h.getSnapshot()).toMatchObject({ open: true, highlighted: 2, draft: "my draft" });
    });

    it("leaves the key to the cursor off the top line", () => {
      const h = withPrompts("a", "b");

      expect(h.keyUp({ onTopLine: false, draft: "x\ny" })).toEqual({ kind: "pass" });

      expect(h.getSnapshot().open).toBe(false);
    });

    it("does nothing with no history", () => {
      const h = new PromptHistory();

      expect(h.keyUp(top())).toEqual({ kind: "pass" });

      expect(h.getSnapshot().open).toBe(false);
    });

    it("moves the highlight up once open, and stays at the oldest", () => {
      const h = withPrompts("a", "b", "c");
      h.keyUp(top());

      h.keyUp({ onTopLine: false, draft: "" });
      expect(h.getSnapshot().highlighted).toBe(1);
      h.keyUp(top());
      h.keyUp(top());
      expect(h.getSnapshot()).toMatchObject({ open: true, highlighted: 0 });
    });
  });

  describe("keyDown", () => {
    it("is the cursor's while closed", () => {
      expect(withPrompts("a").keyDown()).toEqual({ kind: "pass" });
    });

    it("moves the highlight down, and stays at the newest", () => {
      const h = withPrompts("a", "b", "c");
      h.keyUp(top());
      h.keyUp(top());
      h.keyUp(top());

      h.keyDown();
      expect(h.getSnapshot().highlighted).toBe(1);
      h.keyDown();
      expect(h.keyDown()).toEqual({ kind: "handled" });
      expect(h.getSnapshot()).toMatchObject({ open: true, highlighted: 2 });
    });
  });

  describe("select", () => {
    it("fills the highlighted prompt in full, and closes", () => {
      const h = withPrompts("a", "line one\nline two", "c");
      h.keyUp(top("draft"));
      h.keyUp(top());

      expect(h.select()).toEqual({ kind: "fill", text: "line one\nline two" });

      expect(h.getSnapshot().open).toBe(false);
    });

    it("fills the one named, as a click does", () => {
      const h = withPrompts("a", "b", "c");
      h.keyUp(top());

      expect(h.select(0)).toEqual({ kind: "fill", text: "a" });
    });

    it("opens again at the newest after a choice", () => {
      const h = withPrompts("a", "b");
      h.keyUp(top());
      h.keyUp(top());
      h.select();

      h.keyUp(top());

      expect(h.getSnapshot().highlighted).toBe(1);
    });

    it("is the view's own while closed", () => {
      expect(withPrompts("a").select()).toEqual({ kind: "pass" });
    });
  });

  describe("cancel", () => {
    it("closes without a fill, so the draft stays", () => {
      const h = withPrompts("a");
      h.keyUp(top("draft"));

      expect(h.cancel()).toEqual({ kind: "handled" });

      expect(h.getSnapshot().open).toBe(false);
    });

    it("is the view's own while closed", () => {
      expect(withPrompts("a").cancel()).toEqual({ kind: "pass" });
    });
  });

  describe("setPrompts while open", () => {
    it("keeps the highlight on the same prompt as newer ones arrive", () => {
      const h = withPrompts("a", "b", "c");
      h.keyUp(top());
      h.keyUp(top());

      h.setPrompts(["a", "b", "c", "d"]);

      expect(h.getSnapshot().prompts[h.getSnapshot().highlighted]).toBe("b");
    });

    it("closes if the prompts go", () => {
      const h = withPrompts("a");
      h.keyUp(top());

      h.setPrompts([]);

      expect(h.getSnapshot().open).toBe(false);
    });
  });

  describe("subscribing", () => {
    it("tells listeners of a change with a new snapshot, and not of none", () => {
      const h = withPrompts("a");
      const listener = vi.fn();
      const stop = h.subscribe(listener);
      const before = h.getSnapshot();

      h.keyUp(top());
      expect(listener).toHaveBeenCalledTimes(1);
      expect(h.getSnapshot()).not.toBe(before);

      h.setPrompts(["a"]);
      expect(listener).toHaveBeenCalledTimes(1);

      stop();
      h.cancel();
      expect(listener).toHaveBeenCalledTimes(1);
    });
  });
});
