import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourceView } from "./resource_view";

const ready = (data: string[]) => ({ status: "ready" as const, data, error: null });

describe("ResourceView", () => {
  it("announces loading as a status", () => {
    render(
      <ResourceView state={{ status: "loading", data: undefined, error: null }} loading="Loading things…">
        {() => null}
      </ResourceView>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading things…");
  });

  it("shows an error as an alert with a retry that reloads", async () => {
    const reload = vi.fn().mockResolvedValue(undefined);
    render(
      <ResourceView state={{ status: "error", data: undefined, error: "Boom", reload }}>{() => null}</ResourceView>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Boom");
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("shows the empty node when there is nothing to list", () => {
    render(
      <ResourceView state={ready([])} isEmpty={(d) => d.length === 0} empty={<p>Nothing here</p>}>
        {(d) => <p>{d.length} items</p>}
      </ResourceView>,
    );
    expect(screen.getByText("Nothing here")).toBeInTheDocument();
  });

  it("renders children with the data when ready", () => {
    render(<ResourceView state={ready(["a", "b"])}>{(d) => <p>{d.length} items</p>}</ResourceView>);
    expect(screen.getByText("2 items")).toBeInTheDocument();
  });
});
