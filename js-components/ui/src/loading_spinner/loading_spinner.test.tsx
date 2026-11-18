import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import LoadingSpinner from "./loading_spinner";

describe("LoadingSpinner", () => {
  it("renders a status region announcing it's loading", () => {
    render(<LoadingSpinner />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
  });
});
