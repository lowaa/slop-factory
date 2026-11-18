import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { useAuthNavigation, type AuthNavigation } from "./auth_navigation";
import { renderWithRouter } from "./testing";

const ROUTES = { login: "/login", register: "/register", forgotPassword: "/forgot-password" };

function RegisterLink() {
  const { navigate, routes } = useAuthNavigation(ROUTES);
  return (
    <button type="button" onClick={() => navigate(routes.register)}>
      Register
    </button>
  );
}

describe("useAuthNavigation", () => {
  it("navigates in-app, pushing a history entry", async () => {
    const { router } = await renderWithRouter(<RegisterLink />, { initialPath: "/login" });

    await userEvent.click(screen.getByRole("button", { name: "Register" }));

    expect(router.state.location.pathname).toBe("/register");
    expect(router.history.length).toBe(2);
  });

  it("passes the routes map through and keeps the result stable across renders", async () => {
    const seen: AuthNavigation<typeof ROUTES>[] = [];
    function Probe() {
      const [renders, setRenders] = useState(0);
      seen.push(useAuthNavigation(ROUTES));
      return (
        <button type="button" onClick={() => setRenders(renders + 1)}>
          Render again
        </button>
      );
    }
    await renderWithRouter(<Probe />);

    await userEvent.click(screen.getByRole("button", { name: "Render again" }));

    expect(seen.length).toBeGreaterThan(1);
    expect(seen[0].routes).toBe(ROUTES);
    expect(new Set(seen).size).toBe(1);
  });
});
