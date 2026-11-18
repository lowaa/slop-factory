import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Button from "./button";

describe("Button", () => {
  it("defaults to a secondary, medium, non-submitting button", () => {
    render(<Button>Save</Button>);

    const button = screen.getByRole("button", { name: "Save" });
    expect(button).toHaveClass("button", "button--secondary", "button--md");
    expect(button).toHaveAttribute("type", "button");
  });

  it("applies the requested variant and size", () => {
    render(
      <Button variant="primary" size="sm">
        Save
      </Button>,
    );

    expect(screen.getByRole("button")).toHaveClass("button--primary", "button--sm");
  });

  it("keeps a caller's className alongside its own", () => {
    render(<Button className="toolbar__action">Save</Button>);

    expect(screen.getByRole("button")).toHaveClass("button", "toolbar__action");
  });

  // Defaulting to type="button" is the point; submit has to be explicit.
  it("submits its form only when asked to", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <Button>Other</Button>
        <Button type="submit">Save</Button>
      </form>,
    );

    await user.click(screen.getByRole("button", { name: "Other" }));
    expect(onSubmit).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("passes through native props like onClick and disabled", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <>
        <Button onClick={onClick}>Go</Button>
        <Button disabled onClick={onClick}>
          Stop
        </Button>
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Go" }));
    await user.click(screen.getByRole("button", { name: "Stop" }));

    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

// WCAG 2 contrast. Palettes are copied from slop-haus's and another app's
// index.scss (the host apps own the tokens); the hover tint mirrors
// button.scss's color-mix.
function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const ch = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2];
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

function mix(dim: string, accent: string, accentShare: number): string {
  const d = parseInt(dim.slice(1), 16);
  const a = parseInt(accent.slice(1), 16);
  const part = (shift: number) =>
    Math.round(((d >> shift) & 255) * (1 - accentShare) + ((a >> shift) & 255) * accentShare);
  return `#${((part(16) << 16) | (part(8) << 8) | part(0)).toString(16).padStart(6, "0")}`;
}

describe("Button tonal", () => {
  it("renders its variant class", () => {
    render(<Button variant="tonal">Answer</Button>);

    expect(screen.getByRole("button")).toHaveClass("button--tonal");
  });

  const palettes = [
    { name: "slop-haus dark", text: "#35d6ff", dim: "#0b3a63", accent: "#00a8ff" },
    { name: "red light", text: "#b3102b", dim: "#fdeef0", accent: "#c8102e" },
    { name: "red dark", text: "#fb7185", dim: "#2a1519", accent: "#d61f3f" },
  ];
  for (const p of palettes) {
    it(`label meets 4.5:1 at rest and on hover in ${p.name}`, () => {
      expect(contrast(p.text, p.dim)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(p.text, mix(p.dim, p.accent, 0.15))).toBeGreaterThanOrEqual(4.5);
    });
  }
});
