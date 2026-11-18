import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, useLocation } from "@tanstack/react-router";
import { renderWithRouter } from "./testing";
import { TestRouterProvider } from "./test_router_provider";

function Where({ label }: { label: string }) {
  const { href } = useLocation();
  return (
    <p>
      {label} at {href}
    </p>
  );
}

describe("renderWithRouter", () => {
  it("keeps ui inside the same router on a rerender", async () => {
    const { rerender, router } = await renderWithRouter(<Where label="first" />, { initialPath: "/a?x=1" });
    expect(screen.getByText("first at /a?x=1")).toBeInTheDocument();

    await router.navigate({ href: "/b" });
    rerender(<Where label="second" />);

    expect(await screen.findByText("second at /b")).toBeInTheDocument();
  });
});

describe("TestRouterProvider", () => {
  it("renders its children inside a memory router that links can move", async () => {
    render(
      <TestRouterProvider initialPath="/start">
        <Where label="here" />
        <Link to="/elsewhere">Go</Link>
      </TestRouterProvider>,
    );
    expect(await screen.findByText("here at /start")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("link", { name: "Go" }));

    expect(await screen.findByText("here at /elsewhere")).toBeInTheDocument();
  });
});
