import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import RichText, { RichTextMode } from "./rich_text";

describe("RichText", () => {
  it("copies the exact source text from the plain view", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const source = "**not** rendered\n    indented";
    render(
      <RichTextMode render={false}>
        <RichText text={source} />
      </RichTextMode>,
    );

    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(writeText).toHaveBeenCalledWith(source);
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("has no copy button when rendered", () => {
    render(<RichText text="**x**" />);

    expect(screen.queryByRole("button", { name: "Copy" })).not.toBeInTheDocument();
  });
});
