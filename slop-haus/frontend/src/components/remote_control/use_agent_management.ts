import { useState } from "react";
import { apiClient, type Conductor } from "@/api";
import { useConductorLifecycle, type AgentLifecycle, type LifecycleSubject } from "@/components/agent_lifecycle";
import { saveFile } from "@/components/agents_panel";
import { errorMessage } from "@/lib/use_async_action";

type ScriptState =
  | { status: "idle" }
  | { status: "confirming" }
  | { status: "working" }
  | { status: "done"; filename: string }
  | { status: "error"; message: string };

export interface AgentManagement {
  conductor: Conductor;
  lifecycle: AgentLifecycle<LifecycleSubject>;
  editingConfig: boolean;
  setEditingConfig: (editing: boolean) => void;
  script: ScriptState;
  setScript: (state: ScriptState) => void;
  issueScript: () => void;
}

// useAgentManagement is everything the ⋯ menu of one remote control can start:
// the lifecycle (disable, enable, remove), its harness config and a new setup
// script. onChanged is called once the list of remote controls has changed.
export function useAgentManagement(conductor: Conductor, onChanged: () => void): AgentManagement {
  const lifecycle = useConductorLifecycle(conductor, () => onChanged(), conductor.run_by_supervisor_id);
  const [editingConfig, setEditingConfig] = useState(false);
  const [script, setScript] = useState<ScriptState>({ status: "idle" });

  function issueScript() {
    setScript({ status: "working" });
    void apiClient.rotateConductorSetupScript(conductor.id).then(
      ({ filename, script: body }) => {
        saveFile(filename, body);
        setScript({ status: "done", filename });
      },
      (err) => setScript({ status: "error", message: errorMessage(err, "Could not create a new setup script.") }),
    );
  }

  return { conductor, lifecycle, editingConfig, setEditingConfig, script, setScript, issueScript };
}
