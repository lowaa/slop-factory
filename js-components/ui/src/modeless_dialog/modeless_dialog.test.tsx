import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ModelessDialog from "./modeless_dialog";
import DialogDock from "./dialog_dock";

describe("ModelessDialog", () => {
  it("is a non-modal dialog named by its title", () => {
    render(
      <ModelessDialog title="15 Jan 2026" onClose={() => {}}>
        Sodium 137
      </ModelessDialog>,
    );

    const dialog = screen.getByRole("dialog", { name: "15 Jan 2026" });
    expect(dialog).toHaveAttribute("aria-modal", "false");
    expect(dialog).toHaveTextContent("Sodium 137");
    expect(screen.getByRole("heading", { level: 2, name: "15 Jan 2026" })).toBeInTheDocument();
  });

  it("puts its title at the heading level asked for", () => {
    render(<ModelessDialog title="Peek" headingLevel={3} onClose={() => {}} />);

    expect(screen.getByRole("heading", { level: 3, name: "Peek" })).toBeInTheDocument();
  });

  it("closes from its close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<ModelessDialog title="Peek" closeLabel="Close peek" onClose={onClose} />);

    await user.click(screen.getByRole("button", { name: "Close peek" }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // Several can be open; Escape must only close the one the user is in.
  it("closes only the dialog focus is in on Escape, without the press reaching the page", async () => {
    const user = userEvent.setup();
    const closeFirst = vi.fn();
    const closeSecond = vi.fn();
    const pageKeyDown = vi.fn();
    render(
      <div onKeyDown={pageKeyDown}>
        <ModelessDialog title="First" onClose={closeFirst} />
        <ModelessDialog title="Second" onClose={closeSecond} />
      </div>,
    );

    await user.click(screen.getByRole("dialog", { name: "Second" }));
    await user.keyboard("{Escape}");

    expect(closeSecond).toHaveBeenCalledTimes(1);
    expect(closeFirst).not.toHaveBeenCalled();
    expect(pageKeyDown).not.toHaveBeenCalled();
  });

  it("ignores other keys", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<ModelessDialog title="Peek" onClose={onClose} />);

    await user.click(screen.getByRole("dialog"));
    await user.keyboard("{Enter}a");

    expect(onClose).not.toHaveBeenCalled();
  });

  it("takes focus when it mounts with a focus request, and again each time the request changes", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <button type="button">Elsewhere</button>
        <ModelessDialog title="Peek" focusRequest={1} onClose={() => {}} />
      </>,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Elsewhere" }));
    expect(dialog).not.toHaveFocus();

    // Same value: nothing new was asked for, so focus stays where the user put it.
    rerender(
      <>
        <button type="button">Elsewhere</button>
        <ModelessDialog title="Peek" focusRequest={1} onClose={() => {}} />
      </>,
    );
    expect(dialog).not.toHaveFocus();

    rerender(
      <>
        <button type="button">Elsewhere</button>
        <ModelessDialog title="Peek" focusRequest={2} onClose={() => {}} />
      </>,
    );
    expect(dialog).toHaveFocus();
  });

  it("leaves focus alone without a focus request", () => {
    render(<ModelessDialog title="Peek" onClose={() => {}} />);

    expect(screen.getByRole("dialog")).not.toHaveFocus();
  });

  it("reports focus entering it from outside, not focus moving within it", async () => {
    const user = userEvent.setup();
    const onFocusEnter = vi.fn();
    render(
      <>
        <button type="button">Elsewhere</button>
        <ModelessDialog title="Peek" closeLabel="Close peek" onClose={() => {}} onFocusEnter={onFocusEnter}>
          <p>Sodium 137</p>
        </ModelessDialog>
      </>,
    );

    // A click on plain text inside still lands focus in the dialog.
    await user.click(screen.getByText("Sodium 137"));
    expect(screen.getByRole("dialog")).toHaveFocus();
    expect(onFocusEnter).toHaveBeenCalledTimes(1);

    await user.tab();
    expect(screen.getByRole("button", { name: "Close peek" })).toHaveFocus();
    expect(onFocusEnter).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Elsewhere" }));
    await user.click(screen.getByRole("button", { name: "Close peek" }));
    expect(onFocusEnter).toHaveBeenCalledTimes(2);
  });

  it("draws its pip in the colour given, hidden from assistive tech", () => {
    render(<ModelessDialog title="Peek" pipColor="rgb(0, 114, 178)" onClose={() => {}} />);

    const pip = screen.getByTestId("modeless-dialog-pip");
    expect(pip).toHaveStyle({ backgroundColor: "rgb(0, 114, 178)" });
    expect(pip).toHaveAttribute("aria-hidden", "true");
  });

  it("has no pip without a colour", () => {
    render(<ModelessDialog title="Peek" onClose={() => {}} />);

    expect(screen.queryByTestId("modeless-dialog-pip")).not.toBeInTheDocument();
  });
});

describe("DialogDock", () => {
  it("is a labelled landmark holding its dialogs in order", () => {
    render(
      <DialogDock label="Open invoices">
        <ModelessDialog title="First" onClose={() => {}} />
        <ModelessDialog title="Second" onClose={() => {}} />
      </DialogDock>,
    );

    const dock = screen.getByRole("complementary", { name: "Open invoices" });
    expect(within(dock).getAllByRole("dialog").map((d) => d.textContent)).toEqual([
      expect.stringContaining("First"),
      expect.stringContaining("Second"),
    ]);
  });

  it("counts a dialog inside a fragment as content", () => {
    render(
      <DialogDock label="Open invoices">
        <>
          <>{false}</>
          <ModelessDialog title="Nested" onClose={() => {}} />
        </>
      </DialogDock>,
    );

    expect(screen.getByRole("complementary", { name: "Open invoices" })).toContainElement(
      screen.getByRole("dialog", { name: "Nested" }),
    );
  });

  // An empty dock must not hold a column open in the host's layout.
  it("renders nothing when it has no dialogs", () => {
    const { container } = render(
      <DialogDock label="Open invoices">
        {[]}
        {false}
        {null}
        <>{false}</>
        <>
          <>{null}</>
        </>
      </DialogDock>,
    );

    expect(container).toBeEmptyDOMElement();
  });
});
