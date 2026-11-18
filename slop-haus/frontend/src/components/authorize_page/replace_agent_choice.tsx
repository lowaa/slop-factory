import { Dropdown, type DropdownOption } from "@slop-factory/ui";
import type { PickableAgent } from "@/components/agent_picker";

interface ReplaceAgentChoiceProps {
  name: string;
  agents: PickableAgent[];
  // replacing is whether the person has chosen to replace one; replaceId is
  // the one picked, "" until they do.
  replacing: boolean;
  replaceId: string;
  onReplacing: (replacing: boolean) => void;
  onReplaceId: (id: string) => void;
}

const NO_AGENT: DropdownOption = { value: "", label: "Choose an agent…" };

// Offered only with a new agent: it is the new agent that takes over, so the
// old one's name can be reused or left behind in history as it was.
export function ReplaceAgentChoice({ name, agents, replacing, replaceId, onReplacing, onReplaceId }: ReplaceAgentChoiceProps) {
  const picked = agents.find((a) => a.id === replaceId);
  return (
    <fieldset className="authorize-page__replace">
      <label>
        <input
          type="checkbox"
          name={name}
          checked={replacing}
          onChange={(e) => onReplacing(e.target.checked)}
          disabled={agents.length === 0}
        />
        Replace an existing agent
      </label>
      {replacing && (
        <>
          <Dropdown
            label="Agent to replace"
            labelHidden
            value={replaceId}
            options={[NO_AGENT, ...agents.map((a) => ({ value: a.id, label: a.name }))]}
            onChange={onReplaceId}
          />
          <p className="authorize-page__replace-detail">
            {picked ? <strong>{picked.name}</strong> : "The agent you pick"} is disabled and archived when you approve: its
            sign-ins stop working and whatever it holds goes back to the queue. History keeps its name and links it to
            the new agent. The new agent starts with no roles or variant of its own.
          </p>
        </>
      )}
    </fieldset>
  );
}
