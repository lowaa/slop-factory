import { useState } from "react";
import { Button, Tooltip } from "@slop-factory/ui";
import { apiClient, type HarnessConfig, type HarnessSettings } from "@/api";
import MemberName from "@/shared/names/member_name";
import HarnessConfigForm from "@/shared/harness_config_form/harness_config_form";
import { summarise } from "@/domain/harness_labels";

export type HarnessConfigState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; config: HarnessConfig };

interface HarnessConfigSectionProps {
  state: HarnessConfigState;
  // A save moves the revision every agent is compared against, so the
  // panel refetches both the config and the roster.
  onSaved: () => void;
}

// The tenant's defaults: what every agent's harness starts with unless its
// own override (on its row) says otherwise. Applies at each agent's next
// start, which the roster's "stale config" marks point at.
export default function HarnessConfigSection({ state, onSaved }: HarnessConfigSectionProps) {
  const [editing, setEditing] = useState(false);

  async function save(settings: HarnessSettings) {
    await apiClient.saveHarnessConfigDefaults(settings);
    setEditing(false);
    onSaved();
  }

  const summary = state.status === "loaded" ? summarise(state.config.definitions, state.config.defaults?.settings) : [];

  return (
    <section className="agents-panel__section" aria-labelledby="harness-config-heading">
      <h2 id="harness-config-heading">Harness config</h2>
      {state.status === "loading" && <p>Loading harness config…</p>}
      {state.status === "error" && (
        <p role="alert" className="agents-panel__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && (
        <>
          <p className="agents-panel__section-note">
            Every agent's defaults. Each takes them the next time its harness starts, so restart the agents
            marked <em>stale config</em>.{" "}
            {state.config.defaults ? (
              <>
                Revision {state.config.defaults.revision}, saved by{" "}
                <MemberName id={state.config.defaults.updated_by} deleted={state.config.defaults.updated_by_deleted} />{" "}
                {new Date(state.config.defaults.updated_at).toLocaleString()}.
              </>
            ) : (
              "Nothing set yet: each workspace's run.env decides."
            )}
          </p>
          {summary.length > 0 && (
            <ul className="agents-panel__config-summary">
              {summary.map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
          )}
          {editing ? (
            <HarnessConfigForm
              definitions={state.config.definitions}
              saved={state.config.defaults?.settings}
              onSave={save}
              onCancel={() => setEditing(false)}
              label="Default harness config"
            />
          ) : (
            <Tooltip text="Edit the harness settings every agent starts with, unless its own override says otherwise.">
              <Button onClick={() => setEditing(true)}>Edit defaults</Button>
            </Tooltip>
          )}
        </>
      )}
    </section>
  );
}
