import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import SessionBar from "./session_bar";

describe("SessionBar", () => {
  it("names the machine and goes back", async () => {
    const onBack = vi.fn();
    render(<SessionBar title="my-laptop" onBack={onBack} />);

    expect(screen.getByRole("heading", { name: "my-laptop" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("has no menu button without anything to put in the menu", () => {
    render(<SessionBar title="my-laptop" onBack={vi.fn()} />);

    expect(screen.queryByRole("button", { name: "Session menu" })).not.toBeInTheDocument();
  });

  it("opens its menu, and Escape and a press outside close it", async () => {
    render(
      <>
        <p>elsewhere</p>
        <SessionBar title="my-laptop" onBack={vi.fn()}>
          <p>facts</p>
        </SessionBar>
      </>,
    );
    const button = screen.getByRole("button", { name: "Session menu" });
    expect(screen.queryByText("facts")).not.toBeInTheDocument();

    await userEvent.click(button);
    expect(screen.getByText("facts")).toBeInTheDocument();
    expect(button).toHaveAttribute("aria-expanded", "true");

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByText("facts")).not.toBeInTheDocument();
    expect(button).toHaveFocus();

    await userEvent.click(button);
    await userEvent.click(screen.getByText("elsewhere"));
    expect(screen.queryByText("facts")).not.toBeInTheDocument();
  });

  it("stays open for a press inside the menu", async () => {
    render(
      <SessionBar title="my-laptop" onBack={vi.fn()}>
        <p>facts</p>
      </SessionBar>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Session menu" }));
    await userEvent.click(screen.getByText("facts"));

    expect(screen.getByText("facts")).toBeInTheDocument();
  });

  it("shows the session's name with a rename control when given one", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<SessionBar title="my-laptop" rename={{ name: "fix the bug", onSave }} onBack={vi.fn()} />);

    expect(screen.getByRole("heading", { name: "fix the bug" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Rename session" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Session name" }), "!{Enter}");
    expect(onSave).toHaveBeenCalledExactlyOnceWith("fix the bug!");
  });
});
