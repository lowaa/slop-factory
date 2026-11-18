import { useState } from "react";
import { Button, Tooltip } from "@slop-factory/ui";
import { Link } from "@slop-factory/router";
import { apiClient } from "@/api";
import Command from "@/shared/command/command";
import Tabs from "@/shared/tabs/tabs";
import { WorkerNotes } from "@/components/workspace_help";
import { saveFile } from "@/components/agents_panel";
import { shellQuote } from "./shell_quote";
import "./add_slot_manually.scss";
import { errorMessage } from "@/lib/use_async_action";

// The hand-run way to add a slot, at the bottom of Add a machine: the board
// provisions workers from the supervisor's group on the Workers page, so this is for when that
// is off, or for adopting an agent registered by hand.
//
// Two routes, because there are two situations: pointing slop-haus at a repo
// you already have, and starting a workspace from nothing. They differ only
// in where the checkout comes from; both then run the same script, which
// the server generates per agent -- see server/internal/agent/setup_script.go.
//
// The script is what makes this possible at all. `slop setup-workspace`
// needs the slop-haus source and a Go toolchain, which the machine running
// an agent does not have: it has a checkout of some other repo. And the
// script needs nothing filled in, because the server bakes in the address
// it was reached on -- this page's own, which nginx makes a complete
// slop-haus endpoint by proxying /mcp, /hooks/ and /oauth/ (plus the
// well-known discovery documents) as well as /api/.
//
// Nothing here is a bearer credential any more: the script registers the
// agent, writes the workspace, then runs a device sign-in itself (RFC 8628,
// see server/internal/workspace/assets/sh/token.sh's cmd_login) -- it prints a short
// code and pauses, and a human approves it right here on /device. A leaked
// copy of the script is therefore just a script.

const ADD_SLOT = "slop-haus slot add";
const ADD_SLOTS = "slop-haus slot add --count 3";
const ADOPT_SLOT = "slop-haus slot add NAME --setup-script FILE";

type Route = "existing" | "fresh";

const ROUTES: { id: Route; label: string }[] = [
  { id: "existing", label: "Into a repo you already have" },
  { id: "fresh", label: "Start a fresh workspace" },
];

type DownloadState =
  | { status: "idle" }
  | { status: "working" }
  | { status: "done"; filename: string }
  | { status: "error"; message: string };

export default function AddSlotManually() {
  const [route, setRoute] = useState<Route>("existing");
  const [name, setName] = useState("");
  const [download, setDownload] = useState<DownloadState>({ status: "idle" });

  async function handleDownload() {
    const wanted = name.trim();
    if (wanted === "") {
      setDownload({ status: "error", message: "Give the worker a name first." });
      return;
    }

    setDownload({ status: "working" });
    try {
      const { filename, script } = await apiClient.createAgentSetupScript(wanted);
      saveFile(filename, script);
      setDownload({ status: "done", filename });
    } catch (err) {
      setDownload({
        status: "error",
        message: errorMessage(err, "Could not create the setup script."),
      });
    }
  }

  const runCommand =
    download.status === "done" ? `bash ${download.filename}` : "bash slop-haus-setup-<worker>.sh";

  return (
    <details className="add-slot-manually">
      <summary>Add a slot manually</summary>

      <div className="add-slot-manually__body">
        <div className="add-slot-manually__lede">
          <p>
            Normally you use <strong>Add worker</strong> in the machine&rsquo;s group on the Workers page. When the board does not provision workers,
            run the command below on a machine running a supervisor (or add <code>--count N</code> for several). It
            creates the worker and signs it in; the running supervisor picks it up. While the board provisions, the
            command refuses.
          </p>
          <Command value={ADD_SLOT} label="the add slot command" />
          <Command value={ADD_SLOTS} label="the add several slots command" />
        </div>
        <p className="add-slot-manually__lede">
          More in the{" "}
          <Link to="/help/$slug" params={{ slug: "supervisor" }} hash="adding-and-managing-workers">
            supervisor guide
          </Link>
          .
        </p>

        <h3>A hand-run workspace, or adopt a registered agent</h3>
        <div className="add-slot-manually__advanced">
          <p className="add-slot-manually__lede">
            Name a worker here and slop-haus hands you a script that registers it and wires a checkout up to work as
            it. It carries no credential of its own: running it signs the workspace in itself, over a short code you
            approve right here. A supervisor can then adopt the agent it registers (the last step).
          </p>

          <div className="add-slot-manually__mint">
            <label className="add-slot-manually__field">
              <span>Worker name</span>
              <input type="text" value={name} placeholder="builder-1" onChange={(e) => setName(e.target.value)} />
            </label>
            <Tooltip text="Create and download a setup script for a new worker with this name. It carries no secret; running it starts a sign-in you approve here.">
              <Button variant="primary" onClick={handleDownload} disabled={download.status === "working"}>
                {download.status === "working" ? "Creating…" : "Download setup script"}
              </Button>
            </Tooltip>
          </div>

          {download.status === "done" && (
            <p className="add-slot-manually__saved" role="status">
              Saved <code>{download.filename}</code>. Run it in your checkout — it carries no secret, so there is
              nothing in it to protect once you have.
            </p>
          )}
          {download.status === "error" && (
            <p className="add-slot-manually__warning" role="alert">
              {download.message}
            </p>
          )}

          <Tabs items={ROUTES} value={route} onChange={setRoute} label="How to set up the workspace">
            <ol className="add-slot-manually__steps">
              {route === "existing" ? <OwnCheckoutStep /> : <MakeWorkspaceStep name={name.trim() || "builder-1"} />}
              <RunScriptStep runCommand={runCommand} />
              <DeviceSignInStep />
              <StartStep />
            </ol>
          </Tabs>
        </div>

        <div className="add-slot-manually__notes">
          <WorkerNotes />
        </div>
      </div>
    </details>
  );
}

// Only the first step depends on the route: where the agent's checkout
// comes from. From there on, both run the same script the same way.

function OwnCheckoutStep() {
  return (
    <li>
      <h3>Give it a checkout of its own</h3>
      <p>
        One worker per checkout: they work in parallel and coordinate through slop-haus, so two workers in one
        directory would be two workers editing the same files. Give each one a directory of its own; the setup
        script clones the manifest&rsquo;s repos into it.
      </p>
    </li>
  );
}

function MakeWorkspaceStep({ name }: { name: string }) {
  // The server takes any non-empty name, and this is a command to paste
  // into a shell. The ~/ stays outside the quotes so it still expands.
  const dir = `~/agents/${shellQuote(name)}`;
  const make = [`mkdir -p ${dir}`, `cd ${dir}`].join("\n");

  return (
    <li>
      <h3>Make somewhere for it to work</h3>
      <p>
        This directory is slop-haus&rsquo;s, not a repo&rsquo;s: each repo is cloned into a subfolder of it. Keep it
        outside any other repository: a workspace nested in one confuses which project root Claude Code reads its
        settings from, and the inner config is quietly ignored.
      </p>
      <Command value={make} label="the directory commands" />
    </li>
  );
}

function RunScriptStep({ runCommand }: { runCommand: string }) {
  return (
    <li>
      <h3>Run the script there</h3>
      <p>
        It writes the MCP config, its hooks and the working agreement (the root <code>AGENTS.md</code>, with a{" "}
        <code>CLAUDE.md</code> that imports it), then clones the manifest&rsquo;s repos into subfolders. Nothing
        is written inside a repo.
      </p>
      <Command value={runCommand} label="the run command" />
    </li>
  );
}

// The workspace signs in the same way however it was set up, and that one
// sign-in covers Claude Code's MCP connection too (.mcp.json's
// headersHelper asks token.sh).
function DeviceSignInStep() {
  return (
    <li>
      <h3>Approve the device sign-in it starts</h3>
      <p>
        The script pauses partway through and prints a short code and a URL. Follow the URL, or open{" "}
        <a href="/device">/device</a> yourself and type the code in by hand — either way you pick which agent it
        signs in as (this one, already filled in) and approve it. The script then finishes on its own.
      </p>
    </li>
  );
}

function StartStep() {
  return (
    <li>
      <h3>Let a supervisor start it</h3>
      <p>
        Nobody starts a worker by hand: a supervisor runs one headless run per ticket, in a checkout it manages, on
        the harness login and config of the machine it runs on. To have it work this agent, adopt it on a machine
        that runs a supervisor (it signs the agent in itself), with the setup script saved above:
      </p>
      <Command value={ADOPT_SLOT} label="the adopt command" />
      <p>
        A bare <code>slop-haus slot add</code> creates a <em>new</em> agent instead.
      </p>
    </li>
  );
}
