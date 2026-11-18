import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Tooltip, { TOOLTIP_HOVER_DELAY_MS } from "./tooltip";

describe("Tooltip", () => {
  it("is hidden until the trigger is hovered", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip text="Explains the button" hoverDelayMs={0}>
        <button type="button">Do it</button>
      </Tooltip>,
    );

    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

    await user.hover(screen.getByRole("button", { name: "Do it" }));
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Explains the button");

    await user.unhover(screen.getByRole("button", { name: "Do it" }));
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it("shows on keyboard focus and hides on blur", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Tooltip text="Explains the button" hoverDelayMs={0}>
          <button type="button">Do it</button>
        </Tooltip>
        <button type="button">Elsewhere</button>
      </>,
    );

    await user.tab();
    expect(screen.getByRole("button", { name: "Do it" })).toHaveFocus();
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Explains the button");

    await user.tab();
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it("links the trigger to the tooltip via aria-describedby only while shown", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip text="Explains the button" hoverDelayMs={0}>
        <button type="button">Do it</button>
      </Tooltip>,
    );
    const trigger = screen.getByRole("button", { name: "Do it" });

    expect(trigger).not.toHaveAttribute("aria-describedby");

    await user.hover(trigger);
    const tooltip = await screen.findByRole("tooltip");
    expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

    await user.unhover(trigger);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(trigger).not.toHaveAttribute("aria-describedby");
  });

  it("dismisses on Escape without requiring the trigger to lose focus", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip text="Explains the button" hoverDelayMs={0}>
        <button type="button">Do it</button>
      </Tooltip>,
    );
    const trigger = screen.getByRole("button", { name: "Do it" });

    trigger.focus();
    expect(await screen.findByRole("tooltip")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  describe("hover delay", () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    function renderDelayed(delay?: number) {
      vi.useFakeTimers();
      render(
        <Tooltip text="Explains the button" hoverDelayMs={delay}>
          <button type="button">Do it</button>
        </Tooltip>,
      );
      return screen.getByRole("button", { name: "Do it" });
    }

    it("waits the default delay after hover before showing", () => {
      const trigger = renderDelayed();

      fireEvent.mouseEnter(trigger);
      act(() => vi.advanceTimersByTime(TOOLTIP_HOVER_DELAY_MS - 1));
      expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

      act(() => vi.advanceTimersByTime(1));
      expect(screen.getByRole("tooltip")).toHaveTextContent("Explains the button");
    });

    it("uses the delay it is given instead of the default", () => {
      const trigger = renderDelayed(200);

      fireEvent.mouseEnter(trigger);
      act(() => vi.advanceTimersByTime(200));
      expect(screen.getByRole("tooltip")).toBeInTheDocument();
    });

    it("shows on focus with no delay", () => {
      const trigger = renderDelayed();

      act(() => trigger.focus());
      expect(screen.getByRole("tooltip")).toBeInTheDocument();
    });

    it("never shows if the pointer leaves before the delay", () => {
      const trigger = renderDelayed();

      fireEvent.mouseEnter(trigger);
      act(() => vi.advanceTimersByTime(TOOLTIP_HOVER_DELAY_MS - 1));
      fireEvent.mouseLeave(trigger);
      act(() => vi.advanceTimersByTime(TOOLTIP_HOVER_DELAY_MS));
      expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    });

    it("shows on hover over a disabled trigger", () => {
      vi.useFakeTimers();
      render(
        <Tooltip text="Explains the button">
          <button type="button" disabled>
            Do it
          </button>
        </Tooltip>,
      );

      fireEvent.mouseEnter(screen.getByRole("button", { name: "Do it" }).parentElement!);
      act(() => vi.advanceTimersByTime(TOOLTIP_HOVER_DELAY_MS));
      expect(screen.getByRole("tooltip")).toBeInTheDocument();
    });
  });

  describe("keeping on-screen", () => {
    afterEach(() => {
      vi.restoreAllMocks();
    });

    // jsdom has no layout, so the geometry is stubbed: the bubble sits
    // 100..340 wherever it is, and the viewport width is what changes.
    // The real-browser geometry is covered by tooltip.stories.tsx.
    function stubLayout(viewportWidth: { current: number }) {
      vi.spyOn(document.documentElement, "clientWidth", "get").mockImplementation(() => viewportWidth.current);
      vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
        DOMRect.fromRect({ x: 100, y: 0, width: 240, height: 20 }),
      );
    }

    it("re-places an open bubble when the viewport is resized", async () => {
      const viewportWidth = { current: 1000 };
      stubLayout(viewportWidth);
      const user = userEvent.setup();
      render(
        <Tooltip text="Explains the button" hoverDelayMs={0}>
          <button type="button">Do it</button>
        </Tooltip>,
      );

      await user.hover(screen.getByRole("button", { name: "Do it" }));
      const tooltip = await screen.findByRole("tooltip");
      expect(tooltip.style.getPropertyValue("--tooltip-shift")).toBe("0px");

      viewportWidth.current = 300;
      fireEvent(window, new Event("resize"));
      expect(tooltip.style.getPropertyValue("--tooltip-shift")).toBe("-48px");
    });

    it("stops listening for resizes once closed", async () => {
      const removeListener = vi.spyOn(window, "removeEventListener");
      stubLayout({ current: 1000 });
      const user = userEvent.setup();
      render(
        <Tooltip text="Explains the button" hoverDelayMs={0}>
          <button type="button">Do it</button>
        </Tooltip>,
      );
      const trigger = screen.getByRole("button", { name: "Do it" });

      await user.hover(trigger);
      await screen.findByRole("tooltip");
      await user.unhover(trigger);

      expect(removeListener).toHaveBeenCalledWith("resize", expect.any(Function));
    });
  });
});
