import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import Timestamp from "./timestamp";

describe("Timestamp", () => {
  it("shows the instant to the minute in the viewer's locale, keeping the exact instant on the element", () => {
    const at = "2026-03-04T05:06:37Z";
    render(<Timestamp at={at} />);

    const el = screen.getByText(
      new Date(at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }),
    );
    expect(el.tagName).toBe("TIME");
    expect(el).toHaveAttribute("datetime", at);
    expect(el).toHaveAttribute("title", new Date(at).toLocaleString());
    expect(el.textContent).not.toContain("37");
  });

  it("renders nothing for a value that is not a time", () => {
    const { container } = render(<Timestamp at="not a time" />);
    expect(container).toBeEmptyDOMElement();
  });
});
