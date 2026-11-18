import { useEffect, useId, useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Conductor, type HarnessConfig, type HarnessSettings } from "@/api";
import { AgentLifecycleActions, AgentLifecycleFeedback } from "@/components/agent_lifecycle";
import HarnessConfigForm from "@/shared/harness_config_form/harness_config_form";
import type { AgentManagement } from "./use_agent_management";
import { useDismissMenu } from "./use_dismiss_menu";
import "./agent_management.scss";
import { errorMessage } from "@/lib/use_async_action";

// AgentMenu is the ⋯ button of a remote control's heading and what it holds.
// Choosing an item closes it; confirmations and failures show in
// AgentManagementNotes, where there is room.
export function AgentMenu({ management }: { management: AgentManagement }) {
  const { conductor, lifecycle, setEditingConfig, setScript } = management;
  const panelId = useId();
  const { open, setOpen, rootRef, buttonRef } = useDismissMenu();
  const disabled = conductor.disabled_at !== null;
  return (
    <div className="agent-menu" ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="agent-menu__button"
        aria-label={`Manage ${conductor.name}`}
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen(!open)}
      >
        ⋯
      </button>
      {open && (
        // A click on any item closes the menu; the item's own handler has run by then.
        <div id={panelId} className="agent-menu__panel" onClick={() => setOpen(false)}>
          <Button size="sm" variant="secondary" onClick={() => setEditingConfig(true)}>
            Edit config
          </Button>
          {/* The supervisor holds its credential and makes a new one itself: a script would only sign it out. */}
          {!disabled && !conductor.run_by_supervisor_id && (
            <Button size="sm" variant="secondary" onClick={() => setScript({ status: "confirming" })}>
              New setup script
            </Button>
          )}
          <AgentLifecycleActions lifecycle={lifecycle} />
        </div>
      )}
    </div>
  );
}

export function AgentManagementNotes({ management }: { management: AgentManagement }) {
  const { conductor, lifecycle, editingConfig, setEditingConfig, script, setScript, issueScript } = management;
  const confirming = conductor.disabled_at === null && (script.status === "confirming" || script.status === "working");
  return (
    <div className="agent-notes">
      {editingConfig && <ConfigEditor conductor={conductor} onClose={() => setEditingConfig(false)} />}
      {confirming && (
        <div className="agent-notes__confirm" role="group" aria-label={`Confirm new setup script for ${conductor.name}`}>
          <p>
            This signs out every workspace running as <strong>{conductor.name}</strong> right away. Nothing can act as it again until
            you run the new script and approve its sign-in.
          </p>
          <Button size="sm" variant="danger" onClick={issueScript} disabled={script.status === "working"}>
            {script.status === "working" ? "Signing out…" : "Sign out and download"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setScript({ status: "idle" })} disabled={script.status === "working"}>
            Cancel
          </Button>
        </div>
      )}
      {script.status === "done" && (
        <p role="status">
          Saved <code>{script.filename}</code>. Run it on {conductor.name} and approve the sign-in it starts.
        </p>
      )}
      {script.status === "error" && (
        <p role="alert" className="agent-notes__error">
          {script.message}
        </p>
      )}
      <AgentLifecycleFeedback lifecycle={lifecycle} />
    </div>
  );
}

type Loaded = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; config: HarnessConfig };

// ConfigEditor edits the remote control's own harness config over the
// tenant's defaults, as the setup screen does.
function ConfigEditor({ conductor, onClose }: { conductor: Conductor; onClose: () => void }) {
  const [loaded, setLoaded] = useState<Loaded>({ status: "loading" });
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    apiClient.getHarnessConfig().then(
      (config) => live && setLoaded({ status: "ready", config }),
      (err) => live && setLoaded({ status: "error", message: errorMessage(err, "Could not load the harness config.") }),
    );
    return () => {
      live = false;
    };
  }, []);

  if (loaded.status === "loading") return <p role="status">Loading config…</p>;
  if (loaded.status === "error") {
    return (
      <p role="alert" className="agent-notes__error">
        {loaded.message}
      </p>
    );
  }
  const { config } = loaded;
  const override = config.overrides.find((o) => o.agent_id === conductor.id);

  async function act(action: () => Promise<void>) {
    setSaveError(null);
    try {
      await action();
      onClose();
    } catch (err) {
      setSaveError(errorMessage(err, "Could not save the config."));
    }
  }

  return (
    <div className="agent-notes__config">
      <HarnessConfigForm
        definitions={config.definitions}
        saved={override?.settings}
        inherited={config.defaults?.settings}
        onSave={(settings: HarnessSettings) => act(async () => void (await apiClient.saveAgentHarnessConfig(conductor.id, settings)))}
        onCancel={onClose}
        label={`Harness config for ${conductor.name}`}
      />
      {override && (
        <Button size="sm" variant="secondary" onClick={() => void act(() => apiClient.removeAgentHarnessConfig(conductor.id))}>
          Reset to tenant defaults
        </Button>
      )}
      {saveError && (
        <p role="alert" className="agent-notes__error">
          {saveError}
        </p>
      )}
    </div>
  );
}
