import { Dropdown, type DropdownOption } from "@slop-factory/ui";
import type { PickableAgent } from "./pickable_agents";
import type { AgentChoiceState } from "./use_agent_choice";
import "./agent_picker.scss";

// The "act as which agent?" question, asked by both the OAuth consent page
// and the device sign-in page -- one feature from the human's side, so one
// component rather than two copies drifting apart.

interface AgentPickerProps {
  // name is the radio group's; two pickers on one page must not share one.
  name: string;
  agents: PickableAgent[];
  state: AgentChoiceState;
}

const NO_AGENT: DropdownOption = { value: "", label: "Choose an agent…" };

function agentOptions(agents: PickableAgent[]): DropdownOption[] {
  return [NO_AGENT, ...agents.map((agent) => ({ value: agent.id, label: agent.name }))];
}

export function AgentPicker({ name, agents, state }: AgentPickerProps) {
  return (
    <fieldset className="agent-picker">
      <legend>Act as</legend>
      <label className="agent-picker__choice">
        <input
          type="radio"
          name={name}
          value="existing"
          checked={state.choice === "existing"}
          onChange={() => state.setChoice("existing")}
          disabled={agents.length === 0}
        />
        An existing agent
      </label>
      {state.choice === "existing" && (
        <div className="agent-picker__select">
          <Dropdown
            label="Existing agent"
            labelHidden
            value={state.agentId}
            options={agentOptions(agents)}
            onChange={state.setAgentId}
          />
        </div>
      )}

      <label className="agent-picker__choice">
        <input
          type="radio"
          name={name}
          value="new"
          checked={state.choice === "new"}
          onChange={() => state.setChoice("new")}
        />
        A new agent
      </label>
      {state.choice === "new" && (
        <input
          type="text"
          aria-label="New agent name"
          placeholder="builder-1"
          value={state.agentName}
          onChange={(e) => state.setAgentName(e.target.value)}
        />
      )}
    </fieldset>
  );
}
