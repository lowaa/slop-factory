import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import CopyButton from "./copy_button";

describe("CopyButton", () => {
  it("puts the value on the clipboard and confirms it", async () => {
    const user = userEvent.setup();
    render(<CopyButton value="slop setup-workspace -name builder-1" label="the setup command" />);

    await user.click(screen.getByRole("button", { name: "Copy the setup command" }));

    expect(await navigator.clipboard.readText()).toBe("slop setup-workspace -name builder-1");
    expect(screen.getByRole("button", { name: /Copy/ })).toHaveTextContent("Copied");
  });

  // Several of these on a page would otherwise all be called "Copy",
  // which is no help to anyone navigating by control.
  it("names what it copies", () => {
    render(<CopyButton value="x" label="the agent token" />);

    expect(screen.getByRole("button", { name: "Copy the agent token" })).toBeInTheDocument();
  });

  // navigator.clipboard exists only in a secure context -- https or
  // localhost -- and slop-haus is often served over plain http on a private host. So
  // this is the path the button actually takes where it is deployed, not
  // a fallback for an exotic browser.
  it("falls back to selection when the clipboard API is unavailable", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("insecure context"));
    const execCommand = vi.fn().mockReturnValue(true);
    vi.stubGlobal("document", Object.assign(document, { execCommand }));

    render(<CopyButton value="the command" label="the setup command" />);
    await user.click(screen.getByRole("button", { name: /Copy/ }));

    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(screen.getByRole("button", { name: /Copy/ })).toHaveTextContent("Copied");
  });

  // A button that silently does nothing leaves someone pasting a command
  // they never copied.
  it("says so when neither route works", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"));
    vi.stubGlobal("document", Object.assign(document, { execCommand: vi.fn().mockReturnValue(false) }));

    render(<CopyButton value="x" label="the setup command" />);
    await user.click(screen.getByRole("button", { name: /Copy/ }));

    expect(screen.getByRole("button", { name: /Copy/ })).toHaveTextContent("Select and copy");
  });
});
