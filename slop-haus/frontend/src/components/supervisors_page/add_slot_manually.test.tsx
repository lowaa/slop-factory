import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import { stubDownloads } from "@/test/stub_downloads";
import AddSlotManually from "./add_slot_manually";

const SUMMARY = "Add a slot manually";

beforeEach(() => {
  vi.restoreAllMocks();
});

async function openAdvanced(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByText(SUMMARY));
}

describe("setup instructions", () => {
  // The whole path for someone with a repo and no slop-haus checkout:
  // name it, get a script, run it there.
  it("registers the agent and saves the script the server named", async () => {
    const user = userEvent.setup();
    const clicked = stubDownloads();
    vi.spyOn(apiClient, "createAgentSetupScript").mockResolvedValue({
      filename: "slop-haus-setup-builder-9.sh",
      script: "#!/usr/bin/env bash\necho hi\n",
    });

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);
    await user.type(screen.getByLabelText("Worker name"), "builder-9");
    await user.click(screen.getByRole("button", { name: "Download setup script" }));

    expect(apiClient.createAgentSetupScript).toHaveBeenCalledWith("builder-9");
    expect(clicked).toHaveLength(1);
    expect(clicked[0].filename).toBe("slop-haus-setup-builder-9.sh");
    expect(await screen.findByRole("status")).toHaveTextContent("slop-haus-setup-builder-9.sh");
  });

  // The script no longer carries a credential, but it is still not
  // something to put on screen: an agent name and this server's address
  // are still nobody else's business to read over a shoulder.
  it("never puts the script itself on the page", async () => {
    const user = userEvent.setup();
    stubDownloads();
    vi.spyOn(apiClient, "createAgentSetupScript").mockResolvedValue({
      filename: "slop-haus-setup-builder-9.sh",
      script: "#!/usr/bin/env bash\necho super-secret-marker\n",
    });

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);
    await user.type(screen.getByLabelText("Worker name"), "builder-9");
    await user.click(screen.getByRole("button", { name: "Download setup script" }));

    await screen.findByRole("status");
    expect(document.body.innerHTML).not.toContain("super-secret-marker");
    expect(document.body.textContent ?? "").not.toContain("super-secret-marker");
  });

  it("asks for a name before calling the server", async () => {
    const user = userEvent.setup();
    const script = vi.spyOn(apiClient, "createAgentSetupScript");

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);
    await user.click(screen.getByRole("button", { name: "Download setup script" }));

    expect(script).not.toHaveBeenCalled();
    expect(await screen.findByRole("alert")).toHaveTextContent("name");
  });

  // Names are unique, so the second script for one name is refused --
  // which is the right answer, and has to be readable.
  it("surfaces the server's refusal", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createAgentSetupScript").mockRejectedValue(
      new ApiError(422, "agent: name already taken: builder-9"),
    );

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);
    await user.type(screen.getByLabelText("Worker name"), "builder-9");
    await user.click(screen.getByRole("button", { name: "Download setup script" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("name already taken");
  });

  // Two situations, differing only in where the checkout comes from. The
  // existing-repo one is the default because it is the common case.
  it("shows the existing-repo route first, and can switch to a fresh one", async () => {
    const user = userEvent.setup();

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);

    expect(screen.getByRole("heading", { name: "Give it a checkout of its own" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Make somewhere for it to work" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Start a fresh workspace" }));

    expect(screen.getByRole("heading", { name: "Make somewhere for it to work" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Give it a checkout of its own" })).not.toBeInTheDocument();
  });

  it("follows either route with the same run, sign-in and start steps", async () => {
    const user = userEvent.setup();

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);

    const sharedSteps = ["Run the script there", "Approve the device sign-in it starts", "Let a supervisor start it"];
    const steps = () => {
      const list = screen.getByRole("heading", { name: "Let a supervisor start it" }).closest("ol")!;
      return {
        headings: within(list)
          .getAllByRole("heading", { level: 3 })
          .map((h) => h.textContent),
        // Everything after the route's own first step.
        sharedText: Array.from(list.children)
          .slice(1)
          .map((li) => li.textContent),
      };
    };

    const existing = steps();
    expect(existing.headings).toEqual(["Give it a checkout of its own", ...sharedSteps]);

    await user.click(screen.getByRole("tab", { name: "Start a fresh workspace" }));

    const fresh = steps();
    expect(fresh.headings).toEqual(["Make somewhere for it to work", ...sharedSteps]);
    expect(fresh.sharedText).toEqual(existing.sharedText);
  });

  // A typical name reads as typed; any other still has to make one
  // directory named exactly that when the command is pasted into a shell.
  it.each([
    ["builder-9", "mkdir -p ~/agents/builder-9\ncd ~/agents/builder-9"],
    [`it's "x"; touch pwned $(touch pwned)`, null],
  ])("puts the agent name %s into the fresh-workspace commands as one word", async (name, expected) => {
    const user = userEvent.setup();

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);
    await user.type(screen.getByLabelText("Worker name"), name.replace(/[{[]/g, "$&$&"));
    await user.click(screen.getByRole("tab", { name: "Start a fresh workspace" }));

    const step = screen.getByRole("heading", { name: "Make somewhere for it to work" }).closest("li")!;
    const commands = step.querySelector("pre")!.textContent ?? "";
    if (expected !== null) {
      expect(commands).toBe(expected);
    }

    const [mkdir, cd] = commands.split("\n");
    expect(cd.replace(/^cd /, "")).toBe(mkdir.replace(/^mkdir -p /, ""));
    // Evaluated as the shell would, in a scratch directory with a fake
    // HOME: a quoting slip shows up as extra words or a stray file.
    const scratch = mkdtempSync(join(tmpdir(), "add-slot-manually-"));
    try {
      const words = execFileSync("bash", ["-c", mkdir.replace(/^mkdir -p /, "printf '%s\\0' ")], {
        cwd: scratch,
        env: { PATH: process.env.PATH, HOME: "/home/someone" },
      }).toString();
      expect(words).toBe(`/home/someone/agents/${name}\0`);
      expect(readdirSync(scratch)).toEqual([]);
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  });

  it("offers no .gitignore list to copy: the script writes nothing inside a repo", async () => {
    const user = userEvent.setup();

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);

    expect(screen.queryByRole("button", { name: /gitignore/i })).not.toBeInTheDocument();
    const runStep = screen.getByRole("heading", { name: "Run the script there" }).closest("li");
    expect(runStep).toHaveTextContent("Nothing is written inside a repo");
  });

  describe("start step", () => {
    async function openStartStep() {
      const user = userEvent.setup();
      await renderSignedIn(<AddSlotManually />);
      await openAdvanced(user);
      const step = () => screen.getByRole("heading", { name: "Let a supervisor start it" }).closest("li")!;
      return { user, step };
    }

    it("adopts the registered agent, warning that a bare slot add makes a new one", async () => {
      const { step } = await openStartStep();
      expect(step()).toHaveTextContent(/Nobody starts a worker by hand/);
      expect(Array.from(step().querySelectorAll("pre")).map((el) => el.textContent)).toEqual([
        "slop-haus slot add NAME --setup-script FILE",
      ]);
      expect(step()).toHaveTextContent(/bare slop-haus slot add creates a new agent/);
      expect(document.body.textContent).not.toContain("run_worker.sh");
    });

    it("copies exactly the adopt command shown", async () => {
      const { user, step } = await openStartStep();
      const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
      await user.click(within(step()).getByRole("button", { name: "Copy the adopt command" }));
      expect(writeText).toHaveBeenCalledWith("slop-haus slot add NAME --setup-script FILE");
    });
  });

  it("replaces the hand-started workspace notes with the supervisor worker's", async () => {
    const user = userEvent.setup();
    await renderSignedIn(<AddSlotManually />);
    await user.click(await screen.findByText(SUMMARY));
    const notes = document.querySelector(".add-slot-manually__notes")!;
    expect(notes).toHaveTextContent("<root>/secrets.env");
    expect(notes).toHaveTextContent("<root>/run.env");
    expect(notes).toHaveTextContent(/model and effort come from the harness config/i);
    expect(Array.from(notes.querySelectorAll("pre")).map((el) => el.textContent)).toEqual(["slop-haus secret set KEY"]);
    expect(notes.textContent).not.toContain("token.sh");
    expect(notes.textContent).not.toContain("--no-update");
    expect(notes.textContent).not.toContain(".slop-haus/run.env");
  });
});

describe("slot add commands", () => {
  it("is collapsed, and offers slot add and --count for when the board does not provision", async () => {
    await renderSignedIn(<AddSlotManually />);
    const section = document.querySelector<HTMLDetailsElement>("details.add-slot-manually")!;
    expect(section.open).toBe(false);
    expect(section.querySelector("summary")).toHaveTextContent(SUMMARY);
    const cmds = Array.from(section.querySelectorAll(".add-slot-manually__lede .command pre")).map((el) => el.textContent);
    expect(cmds).toEqual(["slop-haus slot add", "slop-haus slot add --count 3"]);
    expect(section).toHaveTextContent(/the command refuses/);
    expect(section).toContainElement(screen.getByRole("button", { name: "Download setup script" }));
    expect(screen.getByRole("link", { name: "supervisor guide" })).toHaveAttribute(
      "href",
      "/help/supervisor#adding-and-managing-workers",
    );
  });
});

// bash -n parses without running: nothing here is executed.
describe("add_slot_manually commands parse as shell", () => {
  it("every command offered for copying is valid shell", async () => {
    const user = userEvent.setup();

    await renderSignedIn(<AddSlotManually />);
    await openAdvanced(user);

    for (const route of ["Into a repo you already have", "Start a fresh workspace"]) {
      await user.click(screen.getByRole("tab", { name: route }));
      const commands = Array.from(document.querySelectorAll<HTMLPreElement>(".command pre")).map(
        (el) => el.textContent ?? "",
      );
      expect(commands.length, `${route} should offer commands`).toBeGreaterThan(0);

      for (const command of commands) {
        expect(() => execFileSync("bash", ["-n"], { input: command }), command).not.toThrow();
      }
    }
  });
});
