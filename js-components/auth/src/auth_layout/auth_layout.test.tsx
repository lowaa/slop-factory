import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TestAuthPagesProvider } from "../test/auth_pages_fixture";
import AuthLayout, { AuthNotice } from "./auth_layout";

describe("AuthLayout", () => {
  it("renders the brand from the surrounding AuthPagesProvider", () => {
    render(
      <TestAuthPagesProvider config={{ brand: "Widgets Inc" }}>
        <AuthLayout title="Sign in">content</AuthLayout>
      </TestAuthPagesProvider>,
    );

    expect(screen.getByText("Widgets Inc")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  it("renders a subtitle when given one", () => {
    render(
      <TestAuthPagesProvider>
        <AuthLayout title="You're invited" subtitle="person@example.com">
          content
        </AuthLayout>
      </TestAuthPagesProvider>,
    );

    expect(screen.getByText("person@example.com")).toBeInTheDocument();
  });

  it("puts the app's layoutClassName on the root, so the app can restyle the card", () => {
    const { container } = render(
      <TestAuthPagesProvider config={{ layoutClassName: "my-app-auth" }}>
        <AuthLayout title="Sign in">content</AuthLayout>
      </TestAuthPagesProvider>,
    );

    expect(container.firstElementChild).toHaveClass("auth-layout", "my-app-auth");
  });

  it("throws when rendered outside an AuthPagesProvider", () => {
    // AuthLayout reads its brand from context -- without a provider it
    // must fail loudly rather than render a blank brand slot.
    expect(() => render(<AuthLayout title="Sign in">content</AuthLayout>)).toThrow(
      /AuthPagesProvider/,
    );
  });
});

describe("AuthNotice", () => {
  it("uses role=alert for an error notice", () => {
    render(
      <TestAuthPagesProvider>
        <AuthNotice variant="error">Something broke</AuthNotice>
      </TestAuthPagesProvider>,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Something broke");
  });

  it.each(["success", "info"] as const)("uses role=status for a %s notice", (variant) => {
    render(
      <TestAuthPagesProvider>
        <AuthNotice variant={variant}>All good</AuthNotice>
      </TestAuthPagesProvider>,
    );

    expect(screen.getByRole("status")).toHaveTextContent("All good");
  });
});
