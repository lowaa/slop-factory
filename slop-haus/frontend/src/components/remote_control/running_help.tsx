import type { ReactNode } from "react";
import Command from "@/shared/command/command";
import HarnessTabs from "@/shared/harness_tabs/harness_tabs";
import type { Harness, HarnessContent } from "@/domain/harnesses";
import { WorkspaceNotes } from "@/components/workspace_help";
import { RUN_ENV, RUN_REMOTE_CONTROL } from "@/components/workspace_help";
import HelpSection from "./help_section";
import "./running_help.scss";

const CONDUCTOR_DIR = ".slop-haus/conductor";

// Everything the two tabs differ in. The host gives each harness a config
// directory of its own (slop-haus-remote-control), which is why the login is a
// separate step.
interface HarnessHelp {
  configDirVar: string;
  configDir: string;
  // Where the owner's own login lives, which the remote control does not use.
  ownConfigDir: string;
  startArgs: string[];
  envAlternative: ReactNode;
  config: ReactNode;
}

const HELP: HarnessContent<HarnessHelp> = {
  claude: {
    configDirVar: "CLAUDE_CONFIG_DIR",
    configDir: `${CONDUCTOR_DIR}/claude-config`,
    ownConfigDir: "~/.claude",
    startArgs: [],
    envAlternative: (
      <>
        Or skip the login and set <code>CLAUDE_CODE_OAUTH_TOKEN</code> (from <code>claude setup-token</code>) or{" "}
        <code>ANTHROPIC_API_KEY</code> in <code>{RUN_ENV}</code>.
      </>
    ),
    config: (
      <>
        The model and effort come from this remote control&rsquo;s <strong>Claude Code</strong> settings under
        &ldquo;Edit config&rdquo;, or from the tenant&rsquo;s defaults if it has none of its own. A{" "}
        <code>--model</code> on the command line wins for that run.
      </>
    ),
  },
  pi: {
    configDirVar: "PI_CODING_AGENT_DIR",
    configDir: `${CONDUCTOR_DIR}/pi-agent`,
    ownConfigDir: "~/.pi/agent",
    startArgs: ["--harness", "pi"],
    envAlternative: (
      <>
        Or, for an API-key provider such as OpenRouter, put the key in <code>{RUN_ENV}</code>: either{" "}
        <code>OPENROUTER_API_KEY=...</code> or <code>&lt; .env OPENROUTER_API_KEY</code> to take just that variable
        from the repo&rsquo;s own <code>.env</code>. The host passes it on.
      </>
    ),
    config: (
      <>
        The provider and model come from this remote control&rsquo;s <strong>pi</strong> settings under
        &ldquo;Edit config&rdquo;, or from the tenant&rsquo;s defaults if it has none of its own, and must be ones the
        login covers: for a ChatGPT login, provider <code>openai</code>. A <code>--provider</code>,{" "}
        <code>--model</code> or <code>--thinking</code> on the command line replaces the config&rsquo;s for that run.
      </>
    ),
  },
  codex: {
    configDirVar: "CODEX_HOME",
    configDir: `${CONDUCTOR_DIR}/codex-home`,
    ownConfigDir: "~/.codex",
    startArgs: ["--harness", "codex"],
    envAlternative: <>Codex signs in with a ChatGPT login only; there is no API key to set.</>,
    config: (
      <>
        The model and effort come from this remote control&rsquo;s <strong>Codex</strong> settings under
        &ldquo;Edit config&rdquo;, or from the tenant&rsquo;s defaults if it has none of its own. The provider is always{" "}
        <code>openai</code>.
      </>
    ),
  },
};

function loginCommand(harness: Harness, help: HarnessHelp): string {
  return `${help.configDirVar}=${help.configDir} ${harness.binary}`;
}

function startCommand(help: HarnessHelp): string {
  return [RUN_REMOTE_CONTROL, ...help.startArgs].join(" ");
}

// RunningHelp says how to start a registered remote control, in the place a
// person registers one.
export default function RunningHelp({ asView = false }: { asView?: boolean }) {
  return (
    <HelpSection className="running-help" title="Running a remote control" asView={asView}>
      <div className="running-help__body">
        <p>
          Run these in the workspace the setup script installed <code>.slop-haus/</code> into. The remote control has a
          login of its own for each harness, once per workspace.
        </p>

        <HarnessTabs>{(harness) => <HarnessTab harness={harness} help={HELP[harness.id]} />}</HarnessTabs>

        <WorkspaceNotes
          subject="remote control"
          lead={
            <li>
              Why the login is separate: the session runs on your machine as you with permissions bypassed, so it gets
              a config directory of its own and none of your settings, plugins, connectors or logins. Log in there
              rather than copying your own credentials in: OAuth refresh tokens rotate, so two copies of one login can
              sign each other out.
            </li>
          }
          signedOutHint={<> (also after &ldquo;New setup script&rdquo;)</>}
          environment={<>The session&rsquo;s environment (API keys, labels)</>}
        />
      </div>
    </HelpSection>
  );
}

function HarnessTab({ harness, help }: { harness: Harness; help: HarnessHelp }) {
  return (
    <ol className="running-help__steps">
      <li>
        <h3>Log in, once per workspace</h3>
        <p>
          This is not your normal <code>{help.ownConfigDir}</code> login. Run it, enter{" "}
          <code>/login</code> inside, then quit.
        </p>
        <Command value={loginCommand(harness, help)} label={`the ${harness.label} login command`} />
        <p>{help.envAlternative}</p>
      </li>
      <li>
        <h3>Start it</h3>
        <Command value={startCommand(help)} label={`the ${harness.label} start command`} />
        <p>{help.config}</p>
      </li>
    </ol>
  );
}
