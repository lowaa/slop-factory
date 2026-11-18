import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HARNESSES } from "@/domain/harnesses";
import RunningHelp from "./running_help";

// The strings a person pastes into a terminal, as literals: a descriptor or
// builder that drifts from the scripts fails here.
const CLAUDE = {
  login: "CLAUDE_CONFIG_DIR=.slop-haus/conductor/claude-config claude",
  start: ".slop-haus/run_remote_control.sh",
};
const PI = {
  login: "PI_CODING_AGENT_DIR=.slop-haus/conductor/pi-agent pi",
  start: ".slop-haus/run_remote_control.sh --harness pi",
};

const copied = vi.hoisted(() => vi.fn());

async function open() {
  const user = userEvent.setup();
  // userEvent.setup installs its own clipboard stub; spy on it.
  vi.spyOn(navigator.clipboard, "writeText").mockImplementation(async (v) => void copied(v));
  render(<RunningHelp />);
  await user.click(screen.getByText("Running a remote control", { selector: "summary" }));
  return user;
}

function shown() {
  return Array.from(document.querySelectorAll(".running-help__steps pre")).map((el) => el.textContent);
}

describe("RunningHelp", () => {
  it("shows Claude Code's login and start by default", async () => {
    await open();
    expect(screen.getByRole("tab", { name: "Claude Code" })).toHaveAttribute("aria-selected", "true");
    expect(shown()).toEqual([CLAUDE.login, CLAUDE.start]);
  });

  it("switches to pi's login and start", async () => {
    const user = await open();
    await user.click(screen.getByRole("tab", { name: "pi" }));
    expect(shown()).toEqual([PI.login, PI.start]);
  });

  it.each([
    ["Claude Code", "the Claude Code login command", CLAUDE.login],
    ["Claude Code", "the Claude Code start command", CLAUDE.start],
    ["pi", "the pi login command", PI.login],
    ["pi", "the pi start command", PI.start],
  ])("%s: %s copies exactly the command shown", async (tab, label, expected) => {
    copied.mockReset();
    const user = await open();
    await user.click(screen.getByRole("tab", { name: tab }));
    await user.click(within(screen.getByRole("tabpanel")).getByRole("button", { name: `Copy ${label}` }));
    expect(copied).toHaveBeenCalledWith(expected);
  });

  it.each(HARNESSES)("has a login and a start command for the registered harness $label", async (harness) => {
    const user = await open();
    await user.click(screen.getByRole("tab", { name: harness.label }));
    expect(screen.getByRole("button", { name: `Copy the ${harness.label} login command` })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: `Copy the ${harness.label} start command` })).toBeInTheDocument();
  });

  it("says restarting picks up updates, and never offers the retired update script", async () => {
    await open();
    const body = document.querySelector(".running-help__body")!;
    expect(body).toHaveTextContent(/restart.*remote\s+control to pick up a new release/);
    expect(body.textContent).not.toMatch(/update-worker/);
    expect(body.textContent).not.toMatch(/refuses to start|won.t start without/);
    expect(body.textContent).toContain(".slop-haus/token.sh login");
    expect(body.textContent).not.toContain("&rsquo;");
  });
});
