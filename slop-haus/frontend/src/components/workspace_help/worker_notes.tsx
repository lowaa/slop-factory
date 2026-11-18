import Command from "@/shared/command/command";

// What to know about a worker a supervisor runs, as opposed to a workspace
// someone starts by hand (WorkspaceNotes). The supervisor owns the
// environment: it does not read the workspace's .slop-haus/run.env.
export default function WorkerNotes() {
  return (
    <>
      <h3>Good to know</h3>
      <ul>
        <li>
          Secrets (API keys and the like) live in <code>&lt;root&gt;/secrets.env</code> on the supervisor&rsquo;s
          machine, set with:
        </li>
      </ul>
      <Command value="slop-haus secret set KEY" label="the secret command" />
      <ul>
        <li>
          Labels are <code>&lt;root&gt;/run.env</code> (<code>SLOP_HAUS_LABELS</code>). Restart the supervisor to pick
          up a change to either file.
        </li>
        <li>
          Model and effort come from the harness config, not from the environment: the worker&rsquo;s own, from the{" "}
          <em>Harness config</em> button on its row, laid over the tenant&rsquo;s defaults, from the{" "}
          <em>Harness config</em> section, one setting at a time.
        </li>
      </ul>
    </>
  );
}
