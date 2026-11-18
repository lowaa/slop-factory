import { useState } from "react";
import { Button, Dropdown, type DropdownOption } from "@slop-factory/ui";
import { apiClient, type Agent, type VariantVersion } from "@/api";
import "../agents_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

function variantOptions(agent: Agent, variants: VariantVersion[]): DropdownOption[] {
  const options: DropdownOption[] = [{ value: "", label: "No variant" }];
  // The list omits archived versions, but one the agent is on is still its
  // current value, and the control must show it.
  if (agent.variant && !variants.some((v) => v.id === agent.variant?.version_id)) {
    options.push({
      value: agent.variant.version_id,
      label: `${agent.variant.name} v${agent.variant.version} (${agent.variant.harness}, archived)`,
    });
  }
  for (const v of variants) {
    if (!v.archived) options.push({ value: v.id, label: `${v.name} v${v.version} (${v.harness})` });
  }
  return options;
}

interface AgentVariantPickerProps {
  agent: Agent;
  variants: VariantVersion[];
  onChanged: () => void;
  onClose: () => void;
}

// AgentVariantPicker is the open editor for an agent's variant; the row
// mounts it while the person is choosing, so the draft choice, the save and
// its error all live here and vanish on close.
export default function AgentVariantPicker({ agent, variants, onChanged, onClose }: AgentVariantPickerProps) {
  const current = agent.variant?.version_id ?? null;
  const [choice, setChoice] = useState<string | null>(current);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function save() {
    setError(null);
    setSaving(true);
    try {
      await apiClient.setAgentVariant(agent.id, choice);
      onClose();
      onChanged();
    } catch (err) {
      setError(errorMessage(err, "Failed to change the variant."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="agents-panel__config-editor" role="group" aria-label={`Variant for ${agent.name}`}>
      <p>
        A variant replaces the tenant&rsquo;s defaults and this agent&rsquo;s own override with its settings, and
        carries its own fleet instructions. It applies at the agent&rsquo;s next start.
      </p>
      <Dropdown
        label="Variant"
        value={choice ?? ""}
        options={variantOptions(agent, variants)}
        onChange={(id) => setChoice(id || null)}
        disabled={saving}
      />
      <div className="agents-panel__role-editor-actions">
        <Button size="sm" variant="primary" onClick={() => void save()} disabled={saving || choice === current}>
          Save
        </Button>
        <Button size="sm" variant="ghost" disabled={saving} onClick={onClose}>
          Cancel
        </Button>
      </div>
      {error && (
        <p role="alert" className="agents-panel__row-note agents-panel__row-note--error">
          {error}
        </p>
      )}
    </div>
  );
}
