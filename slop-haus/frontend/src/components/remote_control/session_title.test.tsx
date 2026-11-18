import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/api";
import SessionTitle from "./session_title";

async function startEdit() {
  await userEvent.click(screen.getByRole("button", { name: "Rename session" }));
  return screen.getByRole("textbox", { name: "Session name" });
}

describe("SessionTitle", () => {
  it("shows the name as the heading, with a Rename button", () => {
    render(<SessionTitle name="fix the bug" onSave={vi.fn()} />);

    expect(screen.getByRole("heading", { name: "fix the bug" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rename session" })).toBeInTheDocument();
  });

  it("saves the trimmed name on Enter", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<SessionTitle name="fix the bug" onSave={onSave} />);
    const input = await startEdit();

    await userEvent.clear(input);
    await userEvent.type(input, "  My name {Enter}");

    expect(onSave).toHaveBeenCalledExactlyOnceWith("My name");
    await waitFor(() => expect(screen.queryByRole("textbox")).not.toBeInTheDocument());
  });

  it("saves on blur", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <>
        <button type="button">elsewhere</button>
        <SessionTitle name="a" onSave={onSave} />
      </>,
    );
    const input = await startEdit();
    await userEvent.type(input, "b");
    await userEvent.click(screen.getByRole("button", { name: "elsewhere" }));

    expect(onSave).toHaveBeenCalledExactlyOnceWith("ab");
  });

  it("cancels on Escape without a request", async () => {
    const onSave = vi.fn();
    render(<SessionTitle name="a" onSave={onSave} />);
    const input = await startEdit();
    await userEvent.type(input, "bcd{Escape}");

    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { name: "a" })).toBeInTheDocument();
  });

  it("sends nothing when the name is unchanged", async () => {
    const onSave = vi.fn();
    render(<SessionTitle name="a" onSave={onSave} />);
    await startEdit();
    await userEvent.keyboard("{Enter}");

    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { name: "a" })).toBeInTheDocument();
  });

  it("clears the custom name when emptied", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<SessionTitle name="Mine" onSave={onSave} />);
    const input = await startEdit();
    await userEvent.clear(input);
    await userEvent.keyboard("{Enter}");

    expect(onSave).toHaveBeenCalledExactlyOnceWith(null);
  });

  it("makes one request for a double Enter, and disables the box meanwhile", async () => {
    let finish: () => void = () => {};
    const onSave = vi.fn().mockReturnValue(new Promise<void>((resolve) => (finish = resolve)));
    render(<SessionTitle name="a" onSave={onSave} />);
    const input = await startEdit();
    await userEvent.type(input, "b");
    // Both land before React has rendered the first save as under way.
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("textbox", { name: "Session name" })).toBeDisabled();
    finish();
    await waitFor(() => expect(screen.queryByRole("textbox")).not.toBeInTheDocument());
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("shows the server's refusal and keeps the edit open", async () => {
    const onSave = vi.fn().mockRejectedValue(new ApiError(422, "The name is too long."));
    render(<SessionTitle name="a" onSave={onSave} />);
    const input = await startEdit();
    await userEvent.type(input, "b{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent("The name is too long.");
    const again = screen.getByRole("textbox", { name: "Session name" });
    expect(again).toBeEnabled();
    expect(again).toHaveValue("ab");
  });
});
