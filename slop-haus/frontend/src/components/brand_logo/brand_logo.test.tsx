import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BrandLogo } from "./brand_logo";

describe("BrandLogo", () => {
  it("renders a decorative image at the requested size with a 2x source", () => {
    const { container } = render(<BrandLogo size={32} />);
    const img = container.querySelector("img.brand-logo");
    expect(img).not.toBeNull();
    expect(img).toHaveAttribute("alt", "");
    expect(img).toHaveAttribute("width", "32");
    expect(img?.getAttribute("srcset")).toMatch(/ 1x, .* 2x$/);
    expect(img?.getAttribute("src")).toMatch(/logo_32/);
  });

  it("uses the larger pair for the auth pages", () => {
    const { container } = render(<BrandLogo size={64} />);
    expect(container.querySelector("img")?.getAttribute("srcset")).toMatch(/logo_128/);
  });
});
