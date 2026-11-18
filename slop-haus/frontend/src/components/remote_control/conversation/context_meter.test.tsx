import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import ContextMeter from "./context_meter";
import { contextLevel } from "@/lib/context_level";

describe("ContextMeter", () => {
  it("shows nothing for unknown (0) context", () => {
    const { container } = render(<ContextMeter tokens={0} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("is neutral below 100k, with no hint", () => {
    render(<ContextMeter tokens={99_000} />);
    expect(screen.getByText("Context: 99k")).toHaveClass("context-meter--neutral");
    expect(screen.queryByText(/consider Clear or Compact/)).not.toBeInTheDocument();
  });

  it("gives no hint at 150k exactly, but does above it", () => {
    const { rerender } = render(<ContextMeter tokens={150_000} />);
    expect(screen.queryByText(/consider Clear or Compact/)).not.toBeInTheDocument();
    rerender(<ContextMeter tokens={214_000} />);
    expect(screen.getByRole("status")).toHaveTextContent("Context: 214k");
    expect(screen.getByText(/consider Clear or Compact/)).toBeInTheDocument();
  });

  it("classifies levels at the thresholds", () => {
    expect(contextLevel(99_999)).toBe("neutral");
    expect(contextLevel(100_000)).toBe("elevated");
    expect(contextLevel(150_000)).toBe("elevated");
    expect(contextLevel(150_001)).toBe("high");
  });
});
