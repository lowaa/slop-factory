import { useMemo, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthPagesProvider, LoginPage, RegisterPage } from "@slop-factory/auth";
import { AuthProvider } from "@/context/auth_context";
import { apiClient, ApiError } from "@/api";
import { makeAuthConfig, makeProfile } from "@/test/fixtures";
import { useSlopHausAuthPagesConfig } from "./auth_pages_config";
import { AUTH_ROUTES } from "./auth_routes";

// Exercises useSlopHausAuthPagesConfig itself -- unlike src/test/with_auth_pages.tsx's
// fake config (for screens that merely need *a* provider ancestor), these
// tests prove the real adapter wires apiClient/useAuth correctly: a
// deliberately broken mapping (wrong register field, hard-coded
// registrationOpen, a describeError that swallows the server's message)
// would fail here even though the shared package's own tests (against a
// fake AuthApi) stay green.
function Harness({ navigate, children }: { navigate: (path: string) => void; children: ReactNode }) {
  const navigation = useMemo(() => ({ navigate, routes: AUTH_ROUTES }), [navigate]);
  const config = useSlopHausAuthPagesConfig(navigation);
  return <AuthPagesProvider config={config}>{children}</AuthPagesProvider>;
}

function renderWithRealConfig(children: ReactNode, navigate = vi.fn()) {
  render(
    <AuthProvider>
      <Harness navigate={navigate}>{children}</Harness>
    </AuthProvider>,
  );
  return { navigate };
}

describe("useSlopHausAuthPagesConfig", () => {
  it("wires LoginPage's submit to apiClient.login via useAuth().signIn", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
    vi.spyOn(apiClient, "login").mockResolvedValue(makeProfile());
    const user = userEvent.setup();
    renderWithRealConfig(<LoginPage />);

    await user.type(await screen.findByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(apiClient.login).toHaveBeenCalledWith("person@example.com", "hunter2pass");
  });

  it("shows the server's ApiError message on a failed sign-in, via describeError", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
    vi.spyOn(apiClient, "login").mockRejectedValue(new ApiError(401, "invalid email or password"));
    const user = userEvent.setup();
    renderWithRealConfig(<LoginPage />);

    await user.type(await screen.findByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid email or password");
  });

  it("reflects the real registrationOpen from GET /api/auth/config", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: false }));
    renderWithRealConfig(<LoginPage />);

    await screen.findByRole("heading", { name: "Sign in" });
    expect(screen.queryByRole("link", { name: "Create one" })).not.toBeInTheDocument();
  });

  it("maps RegisterPage's display name field to apiClient.register's displayName argument", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
    vi.spyOn(apiClient, "register").mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderWithRealConfig(<RegisterPage />);

    await user.type(await screen.findByLabelText("Display name"), "Ada Lovelace");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass1");
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(apiClient.register).toHaveBeenCalledWith("ada@example.com", "hunter2pass1", "Ada Lovelace");
  });

  it("routes the forgot-password link to /forgot-password via the real navigate callback", async () => {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
    const user = userEvent.setup();
    const { navigate } = renderWithRealConfig(<LoginPage />);

    await user.click(await screen.findByRole("link", { name: "Forgot password?" }));

    expect(navigate).toHaveBeenCalledWith("/forgot-password");
  });
});
