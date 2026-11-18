import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { apiClient } from "@/api";
import { stubTouchDevice } from "@/test/layout";
import { stubLocalStorage } from "@/test/storage";
import MessageComposer, { type StopControl } from "./message_composer";
import { expectOneRow } from "./composer_row";

function Harness({
  onSubmit,
  sendDisabled,
  sentPrompts,
  stop,
}: {
  onSubmit: () => void;
  sendDisabled?: boolean;
  sentPrompts?: readonly string[];
  stop?: StopControl;
}) {
  const [value, setValue] = useState("");
  return (
    <MessageComposer value={value} onChange={setValue} onSubmit={onSubmit} sendDisabled={sendDisabled} sentPrompts={sentPrompts} stop={stop} />
  );
}

const KEY = "remote_control.enter_sends";

const box = () => screen.getByRole("textbox", { name: "Message" });
const checkbox = () => screen.getByRole("checkbox", { name: "Enter sends" });

let storage: Storage;

describe("MessageComposer", () => {
  beforeEach(() => {
    storage = stubLocalStorage();
    vi.spyOn(apiClient, "setMyBoolSetting").mockRejectedValue(new Error("the account setting is retired"));
  });

  it("puts the message box and Send in one row, in that order", () => {
    render(<Harness onSubmit={vi.fn()} />);

    expectOneRow(screen.getByRole("textbox", { name: "Message" }), screen.getByRole("button", { name: "Send" }));
  });

  it("submits what was typed with Send", async () => {
    const onSubmit = vi.fn();
    render(<Harness onSubmit={onSubmit} />);

    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "hi");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("leaves Send off when told to", () => {
    render(<Harness onSubmit={vi.fn()} sendDisabled />);

    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  describe("Enter sends", () => {
    it("is on by default on a desktop", () => {
      render(<Harness onSubmit={vi.fn()} />);

      expect(checkbox()).toBeChecked();
      expect(box()).toHaveAttribute("enterkeyhint", "send");
    });

    it("is off by default on a touch device, and the box says so to the keyboard", () => {
      stubTouchDevice();
      render(<Harness onSubmit={vi.fn()} />);

      expect(checkbox()).not.toBeChecked();
      expect(box()).toHaveAttribute("enterkeyhint", "enter");
    });

    it("lets a touch device opt in, and keeps it", async () => {
      stubTouchDevice();
      render(<Harness onSubmit={vi.fn()} />);

      await userEvent.click(checkbox());

      expect(checkbox()).toBeChecked();
      expect(box()).toHaveAttribute("enterkeyhint", "send");
      expect(storage.getItem(KEY)).toBe("on");
    });

    it("writes nothing to the browser or the account until the person chooses", async () => {
      render(<Harness onSubmit={vi.fn()} />);

      expect(storage.getItem(KEY)).toBeNull();
      await userEvent.click(checkbox());
      expect(storage.getItem(KEY)).toBe("off");
      expect(apiClient.setMyBoolSetting).not.toHaveBeenCalled();
    });

    it("sends on Enter when on, without adding a newline", async () => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);

      await userEvent.type(box(), "hi{Enter}");

      expect(onSubmit).toHaveBeenCalledTimes(1);
      expect(box()).toHaveValue("hi");
    });

    it("adds a newline on Shift+Enter when on, and does not send", async () => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);

      await userEvent.type(box(), "a{Shift>}{Enter}{/Shift}b");

      expect(onSubmit).not.toHaveBeenCalled();
      expect(box()).toHaveValue("a\nb");
    });

    it("does not send on Enter while Send is off, and the key is not swallowed", async () => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} sendDisabled />);

      await userEvent.type(box(), "a{Enter}b");

      expect(onSubmit).not.toHaveBeenCalled();
      expect(box()).toHaveValue("a\nb");
    });

    it("adds a newline on Enter when off, and only Send sends", async () => {
      storage.setItem(KEY, "off");
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);

      await userEvent.type(box(), "a{Enter}b");
      expect(onSubmit).not.toHaveBeenCalled();
      expect(box()).toHaveValue("a\nb");

      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    // The Enter that confirms a composition is the IME's: browsers report it
    // as isComposing, or (Safari) as keyCode 229 with isComposing already off.
    it.each([
      ["isComposing", { isComposing: true }],
      ["keyCode 229", { keyCode: 229 }],
    ])("does not send on the Enter that confirms a composition (%s)", (_name, init) => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);

      const notCancelled = fireEvent.keyDown(box(), { key: "Enter", ...init });

      expect(onSubmit).not.toHaveBeenCalled();
      expect(notCancelled, "the IME still gets the key").toBe(true);
    });
  });

  describe("prompt history", () => {
    const LONG = "and this one is really long and has more lines";
    const prompts = ["third last", `${LONG}\nsecond line\nthird line`, "last prompt"];
    const options = () => screen.getAllByRole("option");

    it("opens on Up from an empty box with first lines only, the newest highlighted at the bottom", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);

      await userEvent.type(box(), "{ArrowUp}");

      expect(options().map((o) => o.textContent)).toEqual(["third last", `${LONG}…`, "last prompt"]);
      expect(options()[2]).toHaveAttribute("aria-selected", "true");
      expect(options()[0]).toHaveAttribute("aria-selected", "false");
      expect(screen.queryByText("second line")).not.toBeInTheDocument();
    });

    it("moves the highlight with Up and Down", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);

      await userEvent.type(box(), "{ArrowUp}{ArrowUp}");
      expect(options()[1]).toHaveAttribute("aria-selected", "true");
      await userEvent.keyboard("{ArrowDown}");
      expect(options()[2]).toHaveAttribute("aria-selected", "true");
    });

    it("points the box at the highlighted option, and keeps focus in it", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);
      expect(box()).not.toHaveAttribute("aria-controls");
      expect(box()).not.toHaveAttribute("aria-activedescendant");

      await userEvent.type(box(), "{ArrowUp}");

      const list = screen.getByRole("listbox");
      expect(box()).toHaveAttribute("aria-controls", list.id);
      expect(box()).toHaveAttribute("aria-activedescendant", options()[2].id);
      expect(box()).toHaveFocus();
    });

    it.each([true, false])("Enter fills the full prompt and sends nothing, with Enter sends %s", async (enterSends) => {
      storage.setItem(KEY, enterSends ? "on" : "off");
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} sentPrompts={prompts} />);
      expect((checkbox() as HTMLInputElement).checked).toBe(enterSends);

      await userEvent.type(box(), "{ArrowUp}{ArrowUp}{Enter}");

      expect(box()).toHaveValue(prompts[1]);
      expect((box() as HTMLTextAreaElement).selectionStart).toBe(prompts[1].length);
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("sends on the next Enter as usual", async () => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} sentPrompts={prompts} />);

      await userEvent.type(box(), "{ArrowUp}{Enter}");
      await userEvent.keyboard("{Enter}");

      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it("fills the one clicked, replacing a draft, without sending", async () => {
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} sentPrompts={prompts} />);
      await userEvent.type(box(), "draft{ArrowUp}");

      await userEvent.click(options()[0]);

      expect(box()).toHaveValue("third last");
      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("closes on Escape and on a click outside, leaving the draft alone", async () => {
      render(
        <>
          <p>elsewhere</p>
          <Harness onSubmit={vi.fn()} sentPrompts={prompts} />
        </>,
      );
      await userEvent.type(box(), "{ArrowUp}{Escape}");
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(box()).toHaveValue("");

      await userEvent.keyboard("{ArrowUp}");
      expect(screen.getByRole("listbox")).toBeInTheDocument();
      await userEvent.click(screen.getByText("elsewhere"));
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(box()).toHaveValue("");
    });

    it("moves the cursor as usual from below the top line, with no list", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);
      await userEvent.type(box(), "one{Shift>}{Enter}{/Shift}two");

      const notCancelled = fireEvent.keyDown(box(), { key: "ArrowUp" });

      expect(notCancelled, "the cursor still gets the key").toBe(true);
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    });

    it("does nothing with no history", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={[]} />);

      const notCancelled = fireEvent.keyDown(box(), { key: "ArrowUp" });

      expect(notCancelled).toBe(true);
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    });

    it("is not opened by a key that confirms an IME composition", () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);

      fireEvent.keyDown(box(), { key: "ArrowUp", isComposing: true });
      fireEvent.keyDown(box(), { key: "ArrowUp", keyCode: 229 });

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    });

    it("closes when the person types", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);

      await userEvent.type(box(), "{ArrowUp}x");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(box()).toHaveValue("x");
    });

    it("leaves a draft alone when Escape closes the list", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={prompts} />);

      await userEvent.type(box(), "my draft{ArrowUp}{Escape}");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(box()).toHaveValue("my draft");
    });

    // Whether a send worked is the page's to know: the prompt joins the
    // history when the conversation holds it.
    it("does not add what was submitted until the conversation has it", async () => {
      render(<Harness onSubmit={vi.fn()} sentPrompts={[]} />);
      await userEvent.type(box(), "hello{Enter}");
      await userEvent.clear(box());

      await userEvent.type(box(), "{ArrowUp}");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    });
  });

  describe("stopping a reply", () => {
    it("has a Stop button only when a reply is running", () => {
      const { rerender } = render(<Harness onSubmit={vi.fn()} />);
      expect(screen.queryByRole("button", { name: "Stop this reply" })).not.toBeInTheDocument();

      rerender(<Harness onSubmit={vi.fn()} stop={{ onStop: vi.fn(), stopping: false }} />);
      expect(screen.getByRole("button", { name: "Stop this reply" })).toBeEnabled();
    });

    it("keeps Stop, then Send, in the actions group after the box, in tab order", async () => {
      render(<Harness onSubmit={vi.fn()} stop={{ onStop: vi.fn(), stopping: false }} />);
      const stopButton = screen.getByRole("button", { name: "Stop this reply" });
      const send = screen.getByRole("button", { name: "Send" });

      expect(Array.from(stopButton.closest(".message-composer-actions")!.children)).toEqual([stopButton, send]);
      box().focus();
      await userEvent.tab();
      expect(stopButton).toHaveFocus();
      await userEvent.tab();
      expect(send).toHaveFocus();
    });

    it("stops on a click, with no confirmation", async () => {
      const onStop = vi.fn();
      render(<Harness onSubmit={vi.fn()} stop={{ onStop, stopping: false }} />);
      await userEvent.click(screen.getByRole("button", { name: "Stop this reply" }));

      expect(onStop).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    it("stops on Esc in the box and leaves the draft", async () => {
      const onStop = vi.fn();
      render(<Harness onSubmit={vi.fn()} stop={{ onStop, stopping: false }} />);
      await userEvent.type(box(), "next thing{Escape}");

      expect(onStop).toHaveBeenCalledTimes(1);
      expect(box()).toHaveValue("next thing");
    });

    it("does not stop on Esc from anywhere but the box", async () => {
      const onStop = vi.fn();
      render(
        <>
          <input aria-label="Elsewhere" />
          <Harness onSubmit={vi.fn()} stop={{ onStop, stopping: false }} />
        </>,
      );
      await userEvent.type(screen.getByRole("textbox", { name: "Elsewhere" }), "{Escape}");

      expect(onStop).not.toHaveBeenCalled();
    });

    it("leaves Esc to a dialog that is open", async () => {
      const onStop = vi.fn();
      render(
        <>
          <div role="alertdialog" aria-label="Confirm" />
          <Harness onSubmit={vi.fn()} stop={{ onStop, stopping: false }} />
        </>,
      );
      await userEvent.type(box(), "{Escape}");

      expect(onStop).not.toHaveBeenCalled();
    });

    it("gives Esc to the prompt history when it is open", async () => {
      const onStop = vi.fn();
      render(<Harness onSubmit={vi.fn()} sentPrompts={["one"]} stop={{ onStop, stopping: false }} />);
      await userEvent.type(box(), "{ArrowUp}");
      expect(screen.getByRole("listbox")).toBeInTheDocument();
      await userEvent.keyboard("{Escape}");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(onStop).not.toHaveBeenCalled();
    });

    it("is disabled and says Stopping… once asked, and Esc does not ask again", async () => {
      const onStop = vi.fn();
      render(<Harness onSubmit={vi.fn()} stop={{ onStop, stopping: true }} />);

      expect(screen.getByRole("button", { name: "Stopping…" })).toBeDisabled();
      await userEvent.type(box(), "{Escape}");
      expect(onStop).not.toHaveBeenCalled();
    });

    it("is disabled with the reason when it cannot be used, and Esc does nothing", async () => {
      const onStop = vi.fn();
      render(<Harness onSubmit={vi.fn()} stop={{ onStop, stopping: false, unavailable: "The host isn't running" }} />);

      const button = screen.getByRole("button", { name: "Stop this reply" });
      expect(button).toBeDisabled();
      expect(button).toHaveAccessibleDescription("The host isn't running");
      await userEvent.type(box(), "{Escape}");
      expect(onStop).not.toHaveBeenCalled();
    });
  });

  describe("growing with its content", () => {
    // jsdom lays nothing out, so the content height is given.
    function contentHeight(px: number) {
      vi.spyOn(HTMLTextAreaElement.prototype, "scrollHeight", "get").mockReturnValue(px);
    }
    afterEach(() => vi.restoreAllMocks());

    it("is as tall as its content", async () => {
      contentHeight(120);
      render(<Harness onSubmit={vi.fn()} />);
      await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "x");

      expect(screen.getByRole("textbox", { name: "Message" })).toHaveStyle({ height: "120px" });
    });

    it("stops growing at the maximum, and scrolls from there", async () => {
      contentHeight(900);
      render(<Harness onSubmit={vi.fn()} />);
      await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "x");

      expect(screen.getByRole("textbox", { name: "Message" })).toHaveStyle({ height: "200px" });
    });
  });
});
