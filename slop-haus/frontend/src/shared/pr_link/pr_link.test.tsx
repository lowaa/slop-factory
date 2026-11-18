import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import PrLink, { commitUrl } from "./pr_link";

describe("PrLink", () => {
  it("links to an https URL in a new tab, without handing it the opener", () => {
    render(<PrLink url="https://example.test/org/repo/pull/7" />);

    const link = screen.getByRole("link", {
      name: "https://example.test/org/repo/pull/7",
    });
    expect(link).toHaveAttribute(
      "href",
      "https://example.test/org/repo/pull/7",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute(
      "title",
      "https://example.test/org/repo/pull/7",
    );
  });

  it("uses its children as the link text", () => {
    render(<PrLink url="https://example.test/pull/7">PR</PrLink>);

    expect(screen.getByRole("link", { name: "PR" })).toHaveAttribute(
      "href",
      "https://example.test/pull/7",
    );
  });

  it.each([
    null,
    "",
    "javascript:alert(1)",
    "http://example.test/pull/7",
    "not a url",
  ])("renders nothing for %j", (url) => {
    const { container } = render(<PrLink url={url} />);

    expect(container).toBeEmptyDOMElement();
  });
});

describe("commitUrl", () => {
  it("follows the PR link into its repository", () => {
    expect(commitUrl("https://example.test/org/repo/pull/7", "abc123")).toBe(
      "https://example.test/org/repo/commit/abc123",
    );
  });

  it.each([
    null,
    "http://example.test/org/repo/pull/7",
    "https://example.test/org/repo/issues/7",
    "https://example.test/org/repo/pull/7/files",
  ])("has no link for the PR link %j", (pr) => {
    expect(commitUrl(pr, "abc123")).toBeNull();
  });

  it("has no link for something that is not a hex commit", () => {
    expect(
      commitUrl("https://example.test/org/repo/pull/7", "@/shared/x"),
    ).toBeNull();
  });
});
